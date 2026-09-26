package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobRoundTrip(t *testing.T) {
	in := Job{Version: "1.2.3", FeedURL: "https://x/latest.json", Current: "1.2.2", HostPID: 42, Relaunch: "/x/app", Args: []string{"--flag"}, Archive: "/tmp/a.zip"}
	path, err := WriteJob(in)
	if err != nil {
		t.Fatalf("WriteJob: %v", err)
	}
	defer os.Remove(path)
	if !strings.HasPrefix(filepath.Base(path), tempPrefix) {
		t.Errorf("job file %s does not use the %s prefix", path, tempPrefix)
	}
	out, err := ReadJob(path)
	if err != nil {
		t.Fatalf("ReadJob: %v", err)
	}
	if out.Version != in.Version || out.FeedURL != in.FeedURL || out.HostPID != 42 || out.Relaunch != in.Relaunch || len(out.Args) != 1 || out.Archive != in.Archive {
		t.Errorf("round trip = %+v", out)
	}
}

func TestReadJobRejectsIncomplete(t *testing.T) {
	for _, body := range []string{
		`{"version":"1.0.0"}`,
		`{"version":"1.0.0","relaunch":"/x/app"}`, // neither archive nor feed
	} {
		p := filepath.Join(t.TempDir(), "job.json")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := ReadJob(p); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("%s: err = %v, want incomplete", body, err)
		}
	}
}

func TestProgressReader(t *testing.T) {
	var last, total int64
	pr := &progressReader{r: strings.NewReader(strings.Repeat("x", 1000)), total: 1000, fn: func(d, tot int64) { last, total = d, tot }}
	buf := make([]byte, 300)
	for {
		if _, err := pr.Read(buf); err != nil {
			break
		}
	}
	if last != 1000 || total != 1000 {
		t.Errorf("progress = %d/%d", last, total)
	}
}

func TestWaitExitGoneProcess(t *testing.T) {
	if !WaitExit(0, 0) {
		t.Error("pid 0 should count as gone")
	}
}

func TestCleanDirOnlyTouchesOwnFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dropwarden.old"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "dropwarden-cli.exe.old"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "someone-else.old"), nil, 0o644)
	os.Mkdir(filepath.Join(dir, "."+tempPrefix+"123"), 0o755)
	cleanDir(dir, true)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 1 || left[0] != "someone-else.old" {
		t.Errorf("left = %v, want only someone-else.old", left)
	}
}

func TestFailureMarkerTakenOnce(t *testing.T) {
	dir := t.TempDir()
	if TakeFailure(dir) != nil {
		t.Fatal("no marker yet, want nil")
	}
	if err := WriteFailure(dir, "1.2.3", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	f := TakeFailure(dir)
	if f == nil || f.Version != "1.2.3" || f.Error != "boom" || f.Time.IsZero() {
		t.Fatalf("marker = %+v", f)
	}
	if TakeFailure(dir) != nil {
		t.Error("marker should be deleted after the first take")
	}
}
