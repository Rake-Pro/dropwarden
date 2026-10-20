package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{"1.0.0", "1.0.0", false},
		{"2.0.0", "1.9.9", true},
		{"1.10.0", "1.9.0", true},
		{"1.0.0", "0.0.0-dev", true},
		{"1.2.3", "1.2.3-dev", true},
		{"1.2.3-rc1", "1.2.3", false},
	}
	for _, c := range cases {
		got, err := semverGreater(c.a, c.b)
		if err != nil {
			t.Fatalf("semverGreater(%q,%q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("semverGreater(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if _, err := semverGreater("1.2", "1.0.0"); err == nil {
		t.Error("expected error for malformed version")
	}
}

func TestIsDev(t *testing.T) {
	u, err := New(Config{FeedURL: "http://x/latest.json", Current: "0.0.0-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsDev() {
		t.Error("0.0.0-dev should be a dev build")
	}
	if IsDev("1.2.3") {
		t.Error("1.2.3 is not a dev build")
	}
}

func TestNewRequiresFeed(t *testing.T) {
	if _, err := New(Config{Current: "1.0.0"}); err == nil {
		t.Error("expected error without a feed url")
	}
}

func TestAssetNames(t *testing.T) {
	cases := map[[2]string]string{
		{"darwin", "arm64"}:  "dropwarden_1.2.3_darwin_universal.zip",
		{"windows", "amd64"}: "dropwarden_1.2.3_windows_amd64.zip",
		{"linux", "amd64"}:   "dropwarden_1.2.3_linux_amd64.tar.gz",
	}
	for k, want := range cases {
		if got := assetNameFor(k[0], k[1], "1.2.3"); got != want {
			t.Errorf("assetNameFor(%s,%s) = %s, want %s", k[0], k[1], got, want)
		}
	}
}

// serveFeed stands up a fake downloads host: /latest.json lists one release
// whose assets are served from /files/<name>. sums overrides the sha256 per
// asset name (for the mismatch and missing-sum cases).
func serveFeed(t *testing.T, version string, assets map[string][]byte, sums map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		m := Manifest{
			Name:        "dropwarden",
			Version:     version,
			Tag:         "v" + version,
			PublishedAt: time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC),
			Notes:       "## Added\n- a thing",
		}
		for name, body := range assets {
			sum := sha256.Sum256(body)
			s := hex.EncodeToString(sum[:])
			if o, ok := sums[name]; ok {
				s = o
			}
			m.Assets = append(m.Assets, ManifestAsset{Name: name, URL: srv.URL + "/files/" + name, SHA256: s, Size: int64(len(body))})
		}
		json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := assets[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckAndDownload(t *testing.T) {
	payload := []byte("new binary bytes")
	name := AssetName("1.1.0")
	srv := serveFeed(t, "1.1.0", map[string][]byte{name: payload, "other.zip": []byte("x")}, nil)

	u, err := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Version != "1.1.0" || rel.TagName != "v1.1.0" {
		t.Fatalf("expected release 1.1.0, got %+v", rel)
	}
	if n := rel.Notes(); len(n) != 1 || n[0].Version != "1.1.0" || !strings.Contains(n[0].Body, "a thing") {
		t.Errorf("notes = %+v", n)
	}

	var last int64
	path, err := u.DownloadProgress(context.Background(), rel, func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("downloaded payload mismatch")
	}
	if last != int64(len(payload)) {
		t.Errorf("progress reached %d, want %d", last, len(payload))
	}
	if !strings.HasPrefix(filepath.Base(path), tempPrefix) {
		t.Errorf("temp file %s does not use the %s prefix", path, tempPrefix)
	}
}

func TestCheckUpToDate(t *testing.T) {
	srv := serveFeed(t, "1.0.0", nil, nil)
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel != nil {
		t.Fatalf("expected up-to-date, got %+v", rel)
	}
}

func TestCheckDevBuildSeesRelease(t *testing.T) {
	srv := serveFeed(t, "0.1.0", nil, nil)
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "0.0.0-dev"})
	rel, err := u.Check(context.Background())
	if err != nil || rel == nil {
		t.Fatalf("dev build should see 0.1.0 as newer: rel=%v err=%v", rel, err)
	}
}

func TestCheckFeedMissing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	if _, err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "404") || !errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v, want a 404 feed error wrapping ErrNoRelease", err)
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	name := AssetName("1.1.0")
	srv := serveFeed(t, "1.1.0", map[string][]byte{name: []byte("payload")},
		map[string]string{name: strings.Repeat("0", 64)})
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := tempFiles(t)
	if _, err := u.Download(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if after := tempFiles(t); after != before {
		t.Errorf("mismatched download left a temp file behind (%d -> %d)", before, after)
	}
}

func TestDownloadMissingChecksumFailsClosed(t *testing.T) {
	name := AssetName("1.1.0")
	srv := serveFeed(t, "1.1.0", map[string][]byte{name: []byte("payload")}, map[string]string{name: ""})
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v, want a missing sha256 error", err)
	}
}

func TestDownloadMissingAsset(t *testing.T) {
	srv := serveFeed(t, "1.1.0", map[string][]byte{"unrelated.zip": []byte("x")}, nil)
	u, _ := New(Config{FeedURL: srv.URL + "/latest.json", Current: "1.0.0"})
	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("err = %v, want a missing asset error", err)
	}
}

// tempFiles counts updater temp files in os.TempDir.
func tempFiles(t *testing.T) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), tempPrefix+"*"))
	return len(m)
}

func TestExtractTarGzFlattens(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("#!/bin/sh\necho hi\n")
	if err := tw.WriteHeader(&tar.Header{Name: "nested/dir/dropwarden", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(content)
	tw.Close()
	gz.Close()

	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := extract(archive, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dropwarden"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("extracted content mismatch")
	}
	fi, _ := os.Stat(filepath.Join(dir, "dropwarden"))
	if fi.Mode().Perm()&0o100 == 0 {
		t.Error("extracted file is not executable")
	}
}

// TestApplyFlatReplacesSiblings installs a tar.gz over a fake install dir:
// both binaries are swapped, the old ones are moved aside as .old, and the
// archive is consumed.
func TestApplyFlatReplacesSiblings(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "dropwarden")
	cli := filepath.Join(dir, "dropwarden-cli")
	os.WriteFile(host, []byte("old host"), 0o755)
	os.WriteFile(cli, []byte("old cli"), 0o755)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"dropwarden": "new host", "dropwarden-cli": "new cli"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	archive := filepath.Join(t.TempDir(), "u.tar.gz")
	os.WriteFile(archive, buf.Bytes(), 0o644)

	got, err := applyFlat(archive, host)
	if err != nil {
		t.Fatal(err)
	}
	if got != host {
		t.Errorf("relaunch = %s, want %s", got, host)
	}
	for path, want := range map[string]string{host: "new host", cli: "new cli", host + ".old": "old host", cli + ".old": "old cli"} {
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(path), b, want)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+tempPrefix) {
			t.Errorf("stage dir %s left behind", e.Name())
		}
	}
}

// TestApplyFlatRollsBack forces the second file's rename to fail (a non-empty
// directory squats on dropwarden-cli.old, so moving the cli aside errors) and
// checks the already-swapped host binary is put back.
func TestApplyFlatRollsBack(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "dropwarden")
	cli := filepath.Join(dir, "dropwarden-cli")
	os.WriteFile(host, []byte("old host"), 0o755)
	os.WriteFile(cli, []byte("old cli"), 0o755)
	if err := os.MkdirAll(filepath.Join(cli+".old", "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"dropwarden", "dropwarden-cli"} {
		body := "new " + name
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	archive := filepath.Join(t.TempDir(), "u.tar.gz")
	os.WriteFile(archive, buf.Bytes(), 0o644)

	_, err := applyFlat(archive, host)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want a rolled-back failure", err)
	}
	for path, want := range map[string]string{host: "old host", cli: "old cli"} {
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(path), b, want)
		}
	}
	if _, err := os.Stat(host + ".old"); !os.IsNotExist(err) {
		t.Errorf("host .old still present after rollback: %v", err)
	}
}

func TestSemverGreaterDevChannel(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.3.4-dev.5.abc1234", "0.3.4-dev.4.def5678", true},  // newer dev build
		{"0.3.4-dev.4.abc1234", "0.3.4-dev.5.def5678", false}, // older dev build
		{"0.3.4-dev.4.abc1234", "0.3.4-dev.4.abc1234", false}, // same build
		{"0.3.4", "0.3.4-dev.9.abc1234", true},                // the release supersedes its dev builds
		{"0.3.4-dev.1.abc1234", "0.3.3", true},                // a dev build of the next version is newer than the last release
		{"0.3.3", "0.3.4-dev.1.abc1234", false},               // ... and the stable feed never downgrades it
		{"1.0.0-rc.2", "1.0.0-rc.1", false},                   // non-dev suffixes stay a tie
	}
	for _, c := range cases {
		got, err := semverGreater(c.a, c.b)
		if err != nil {
			t.Fatalf("%s > %s: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("%s > %s = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDevIsLocalBuildOnly(t *testing.T) {
	if !IsDev("0.0.0-dev") || !IsDev("0.0.0-abc") {
		t.Fatal("the unstamped local build must read as dev")
	}
	if IsDev("0.3.4-dev.3.abc1234") || IsDev("0.3.4") {
		t.Fatal("CI dev-channel builds and releases are real versions")
	}
}
