// Package update implements self-update from a static release manifest
// (latest.json) served over plain HTTPS next to the release assets. It is portable
// and CGO-free: Check reads the manifest, Download fetches this platform's
// asset and verifies it against the manifest's sha256 (failing closed), and
// Apply swaps the installed binaries (windows/linux) or the whole .app bundle
// (darwin).
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// checkTimeout bounds one manifest fetch; the manifest is tiny.
const checkTimeout = 10 * time.Second

// maxManifestBytes caps the manifest body (it only ever lists a handful of
// assets plus one release's notes).
const maxManifestBytes = 1 << 20

// tempPrefix names every temp file and stage directory the updater creates.
const tempPrefix = "dropwarden-update-"

// ErrNoRelease is returned (wrapped) by Check when the feed answers 404: no
// release has been published at that URL yet. The host reports it as a
// status, not as an error.
var ErrNoRelease = errors.New("update: no release published yet")

// Manifest is the feed document (latest.json). The release workflow writes it
// next to the assets; see README "Releasing" for the published layout.
type Manifest struct {
	Name        string          `json:"name"`         // "dropwarden"
	Version     string          `json:"version"`      // "0.1.0" (no leading v)
	Tag         string          `json:"tag"`          // "v0.1.0"
	PublishedAt time.Time       `json:"published_at"` // RFC 3339, UTC
	Notes       string          `json:"notes"`        // markdown release notes
	Assets      []ManifestAsset `json:"assets"`
}

// ManifestAsset is one downloadable file of a release.
type ManifestAsset struct {
	Name   string `json:"name"`   // e.g. "dropwarden_0.1.0_windows_amd64.zip"
	URL    string `json:"url"`    // absolute download URL
	SHA256 string `json:"sha256"` // lowercase hex; required, the updater fails closed without it
	Size   int64  `json:"size"`   // bytes; 0 when unknown
}

// Config configures an Updater.
type Config struct {
	FeedURL    string // manifest URL (config.DefaultUpdateFeedURL in production)
	Current    string // running version, e.g. "1.2.3" or "0.0.0-dev"
	HTTPClient *http.Client
}

// Updater checks for, downloads and applies releases.
type Updater struct {
	cfg Config
}

// Release describes an available release.
type Release struct {
	Version     string // "1.4.0"
	TagName     string // "v1.4.0"
	PublishedAt time.Time
	Body        string // markdown notes from the manifest
	assets      []ManifestAsset
}

// New returns an Updater. FeedURL and Current are required.
func New(cfg Config) (*Updater, error) {
	if cfg.FeedURL == "" {
		return nil, fmt.Errorf("update: feed url is required")
	}
	if cfg.Current == "" {
		return nil, fmt.Errorf("update: current version is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Updater{cfg: cfg}, nil
}

// IsDev reports whether the running build is a local build (never auto-updated).
func (u *Updater) IsDev() bool { return IsDev(u.cfg.Current) }

// IsDev reports whether version is a LOCAL build: the unstamped
// version.Version default ("0.0.0-dev"), or any 0.0.0-* stamp. A dev-channel
// build published by CI ("0.3.4-dev.3.abc1234") is a real version that
// updates like any other; see semverGreater for its ordering.
func IsDev(version string) bool { return strings.HasPrefix(version, "0.0.0-") }

// Check fetches the manifest and reports whether it names a release newer
// than the running version. A nil Release with nil error means "up to date".
func (u *Updater) Check(ctx context.Context) (*Release, error) {
	rel, _, err := u.CheckLatest(ctx)
	return rel, err
}

// CheckLatest is Check that also returns the release the manifest names
// (latest), newer or not, so the caller can show the notes of the version it
// is already running. latest is nil only on error.
func (u *Updater) CheckLatest(ctx context.Context) (newer, latest *Release, err error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cfg.FeedURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("update: feed request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("update: check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("%w: no release feed at %s (404)", ErrNoRelease, u.cfg.FeedURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("update: check: unexpected status %s from %s", resp.Status, u.cfg.FeedURL)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxManifestBytes)).Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("update: decode manifest: %w", err)
	}
	ver := strings.TrimPrefix(m.Version, "v")
	if ver == "" {
		ver = strings.TrimPrefix(m.Tag, "v")
	}
	if ver == "" {
		return nil, nil, fmt.Errorf("update: manifest at %s has no version", u.cfg.FeedURL)
	}
	isNewer, err := semverGreater(ver, strings.TrimPrefix(u.cfg.Current, "v"))
	if err != nil {
		return nil, nil, fmt.Errorf("update: compare %q vs %q: %w", ver, u.cfg.Current, err)
	}
	tag := m.Tag
	if tag == "" {
		tag = "v" + ver
	}
	latest = &Release{Version: ver, TagName: tag, PublishedAt: m.PublishedAt, Body: m.Notes, assets: m.Assets}
	if !isNewer {
		return nil, latest, nil
	}
	return latest, latest, nil
}

// Download fetches this platform's asset for rel into a temp file and verifies
// its sha256 against the manifest. It returns the archive path.
func (u *Updater) Download(ctx context.Context, rel *Release) (string, error) {
	return u.DownloadProgress(ctx, rel, nil)
}

// DownloadProgress is Download with a progress callback, called as bytes
// arrive with the running total and the asset size (0 when unknown).
func (u *Updater) DownloadProgress(ctx context.Context, rel *Release, progress func(done, total int64)) (string, error) {
	name := AssetName(rel.Version)
	a, ok := findAsset(rel.assets, name)
	if !ok {
		return "", fmt.Errorf("update: release %s has no asset %q", rel.TagName, name)
	}
	want := strings.ToLower(strings.TrimSpace(a.SHA256))
	if len(want) != 64 {
		return "", fmt.Errorf("update: release %s: asset %s has no valid sha256 in the manifest", rel.TagName, name)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return "", fmt.Errorf("update: release %s: asset %s sha256 is not hex", rel.TagName, name)
	}
	if a.URL == "" {
		return "", fmt.Errorf("update: release %s: asset %s has no url", rel.TagName, name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", fmt.Errorf("update: download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: download %s: unexpected status %s", name, resp.Status)
	}

	tmp, err := os.CreateTemp("", tempPrefix+"*"+archiveExt(name))
	if err != nil {
		return "", fmt.Errorf("update: temp file: %w", err)
	}
	h := sha256.New()
	var src io.Reader = resp.Body
	if progress != nil {
		total := a.Size
		if total <= 0 && resp.ContentLength > 0 {
			total = resp.ContentLength
		}
		src = &progressReader{r: resp.Body, total: total, fn: progress}
	}
	_, err = io.Copy(io.MultiWriter(tmp, h), src)
	cerr := tmp.Close()
	if err != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("update: download %s: copy=%v close=%v", name, err, cerr)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("update: checksum mismatch for %s: got %s want %s", name, got, want)
	}
	log.Info().Str("asset", name).Str("version", rel.Version).Msg("update downloaded and verified")
	return tmp.Name(), nil
}

// AssetName returns the release asset filename the updater expects for the
// running platform. Must stay in sync with .github/workflows/release.yml.
func AssetName(version string) string {
	return assetNameFor(runtime.GOOS, runtime.GOARCH, version)
}

func assetNameFor(goos, goarch, version string) string {
	switch goos {
	case "darwin":
		return fmt.Sprintf("dropwarden_%s_darwin_universal.zip", version)
	case "windows":
		return fmt.Sprintf("dropwarden_%s_windows_%s.zip", version, goarch)
	default:
		return fmt.Sprintf("dropwarden_%s_%s_%s.tar.gz", version, goos, goarch)
	}
}

func findAsset(assets []ManifestAsset, name string) (ManifestAsset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return ManifestAsset{}, false
}

// semverGreater reports whether a > b for "X.Y.Z[-suffix]" versions. A
// suffixed version loses a tie against the same bare version ("1.2.3-dev.4" <
// "1.2.3": the release supersedes every dev build that led to it). Two dev
// builds of the same X.Y.Z order by their build number: "1.2.3-dev.5" >
// "1.2.3-dev.4" (anything after the number, such as the commit, is ignored).
// Other suffix pairs are a tie.
func semverGreater(a, b string) (bool, error) {
	am, as, err := parseSemver(a)
	if err != nil {
		return false, err
	}
	bm, bs, err := parseSemver(b)
	if err != nil {
		return false, err
	}
	for i := range am {
		if am[i] != bm[i] {
			return am[i] > bm[i], nil
		}
	}
	if as == "" || bs == "" {
		return as == "" && bs != "", nil
	}
	an, aok := devBuildNumber(as)
	bn, bok := devBuildNumber(bs)
	return aok && bok && an > bn, nil
}

// devBuildNumber parses the N of a "dev.N[.anything]" pre-release suffix.
func devBuildNumber(suffix string) (int, bool) {
	rest, ok := strings.CutPrefix(suffix, "dev.")
	if !ok {
		return 0, false
	}
	num, _, _ := strings.Cut(rest, ".")
	n, err := strconv.Atoi(num)
	return n, err == nil && n >= 0
}

func parseSemver(v string) ([3]int, string, error) {
	var nums [3]int
	base, suffix, _ := strings.Cut(v, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return nums, "", fmt.Errorf("not X.Y.Z: %q", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", fmt.Errorf("not X.Y.Z: %q", v)
		}
		nums[i] = n
	}
	return nums, suffix, nil
}

// archiveExt returns ".zip" or ".tar.gz" matching the asset name.
func archiveExt(name string) string {
	if strings.HasSuffix(name, ".tar.gz") {
		return ".tar.gz"
	}
	return filepath.Ext(name)
}

// progressReader reports bytes read through fn.
type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.fn(p.done, p.total)
	}
	return n, err
}
