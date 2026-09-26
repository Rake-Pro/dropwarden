package service

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/logging"
	"github.com/rake-pro/dropwarden/internal/version"
)

const (
	// diagLogLines is how many recent log lines the bundle carries.
	diagLogLines = 400
	// diagMaxFile skips any single debug dump larger than this in the export.
	diagMaxFile = 5 << 20
)

// secretKey matches config keys whose values never leave the machine.
var secretKey = regexp.MustCompile(`(?i)(^username$|password|passwd|secret|token|cookie|credential|api_?key|auth_?key|session_?id)`)

// secretValue matches values that look like a bearer/OAuth token wherever
// they appear.
var secretValue = regexp.MustCompile(`^(?i:oauth:|bearer\s)|^[A-Za-z0-9_\-]{30,}$`)

// redactedConfigYAML renders cfg as YAML with credentials and token-like
// values blanked and the home directory shown as "~".
func redactedConfigYAML(cfg *config.Config) string {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return "config: marshal failed: " + err.Error() + "\n"
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return "config: re-parse failed: " + err.Error() + "\n"
	}
	redactNode(&root)
	out, err := yaml.Marshal(&root)
	if err != nil {
		return "config: marshal failed: " + err.Error() + "\n"
	}
	return scrubHome(string(out))
}

func redactNode(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			redactNode(c)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind == yaml.ScalarNode && v.Value != "" && secretKey.MatchString(k.Value) {
				v.Value, v.Tag, v.Style = "", "!!str", 0
				continue
			}
			redactNode(v)
		}
	case yaml.ScalarNode:
		if secretValue.MatchString(n.Value) {
			n.Value, n.Tag, n.Style = "", "!!str", 0
		}
	}
}

// scrubHome replaces the user's home directory (plain and JSON-escaped) with
// "~" so the bundle does not carry the account name in every path.
func scrubHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || len(home) < 2 {
		return s
	}
	s = strings.ReplaceAll(s, home, "~")
	if esc := strings.ReplaceAll(home, `\`, `\\`); esc != home {
		s = strings.ReplaceAll(s, esc, "~")
	}
	return s
}

// Diagnostics renders the plain-text support bundle: build and host, the
// update state, the redacted config, every profile's runtime state and last
// discovery, then the recent log.
func (b *Backend) Diagnostics() string {
	cfg := b.config()
	var sb strings.Builder
	section := func(title string) { fmt.Fprintf(&sb, "\n==== %s ====\n", title) }
	asJSON := func(v any) string {
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err.Error()
		}
		return string(out)
	}

	fmt.Fprintf(&sb, "dropwarden diagnostics - %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "version: %s\nplatform: %s/%s\n", version.Version, runtime.GOOS, runtime.GOARCH)
	chrome, err := browser.FindChrome(cfg.Browser.ChromePath)
	if err != nil {
		fmt.Fprintf(&sb, "chrome: found=false (%v)\n", err)
	} else {
		fmt.Fprintf(&sb, "chrome: found=true path=%s\n", chrome)
	}

	section("update")
	u := b.updateState()
	pending := ""
	if u.Pending != nil {
		pending = u.Pending.Version
	}
	lastCheck := "never"
	if u.LastCheck != nil {
		lastCheck = u.LastCheck.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(&sb, "current=%s status=%s phase=%s pending=%q enabled=%v autoInstall=%v devBuild=%v feed=%s\n",
		u.CurrentVersion, u.Status, u.Phase, pending, u.Enabled, u.AutoInstall, u.DevBuild, u.FeedURL)
	fmt.Fprintf(&sb, "lastCheck=%s lastError=%q deferredReason=%q failedVersion=%q failMessage=%q skippedVersion=%q\n",
		lastCheck, u.LastError, u.DeferredReason, u.FailedVersion, u.FailMessage, u.SkippedVersion)

	section("config (redacted)")
	sb.WriteString(redactedConfigYAML(cfg))

	for _, pc := range cfg.Profiles {
		eff, err := cfg.Effective(pc.Name)
		if err != nil {
			continue
		}
		section(fmt.Sprintf("profile %s (%s) enabled=%v", eff.Name, eff.DisplayName, eff.Enabled))
		pb := b.Profile(eff.Name)
		for _, p := range platformOrder {
			fmt.Fprintf(&sb, "-- %s\n", p)
			if pb == nil {
				sb.WriteString("store not open\n")
				continue
			}
			if le := b.launchError(eff.Name, p); le != "" {
				fmt.Fprintf(&sb, "launchError: %s\n", le)
			}
			if e := pb.Engine(p); e != nil {
				st := e.Status()
				fmt.Fprintf(&sb, "up=true needsLogin=%v state=%s paused=%v reason=%q drop=%q channel=%q progress=%d/%d\n",
					pb.NeedsLogin(p), st.State, st.Paused, st.Reason, st.DropName, st.ChannelLogin, st.Current, st.Required)
				elig := e.LastEligibility()
				keys := make([]string, 0, len(elig))
				for k := range elig {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					parts = append(parts, fmt.Sprintf("%s=%d", k, elig[k]))
				}
				fmt.Fprintf(&sb, "eligibility: %s\n", strings.Join(parts, " "))
			} else {
				fmt.Fprintf(&sb, "up=false wanted=%v needsLogin=%v\n", b.wantsRun(eff.Name, p), b.needsLoginDown(eff.Name, p))
			}
			if diag := lastDiscovery(pb.Discoverer(p)); diag != nil {
				fmt.Fprintf(&sb, "lastDiscovery: %s\n", asJSON(diag))
			}
		}
	}

	section(fmt.Sprintf("log (last %d lines)", diagLogLines))
	for _, l := range logging.RecentLines(diagLogLines) {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return scrubHome(sb.String())
}

// diagnosticsDir is where the export lands: the user's Desktop, else the app
// data dir.
func diagnosticsDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Desktop")
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d, nil
		}
	}
	return config.Dir()
}

// ExportDiagnostics writes dropwarden-diagnostics-<timestamp>.zip (see
// writeDiagnosticsZip) and returns its path.
func (b *Backend) ExportDiagnostics() (string, error) {
	dir, err := diagnosticsDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dropwarden-diagnostics-"+time.Now().Format("20060102-150405")+".zip")
	if err := b.writeDiagnosticsZip(path); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// writeDiagnosticsZip builds the export: diagnostics.txt, the log (and its
// rotated .1), the redacted config, update.log, and every profile's
// debug/*.json (no *.html, nothing over 5 MB). Chrome profile dirs and
// cookies are never read.
func (b *Backend) writeDiagnosticsZip(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	zw := zip.NewWriter(f)
	add := func(name string, r io.Reader) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		return err
	}
	addFile := func(name, src string) error {
		in, err := os.Open(src)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer in.Close()
		return add(name, in)
	}

	cfg := b.config()
	err = add("diagnostics.txt", strings.NewReader(b.Diagnostics()))
	if err == nil {
		err = add("config.yaml", strings.NewReader(redactedConfigYAML(cfg)))
	}
	if logPath, lerr := config.LogPath(); err == nil && lerr == nil {
		if err = addFile("dropwarden.log", logPath); err == nil {
			err = addFile("dropwarden.log.1", logPath+".1")
		}
	}
	if dir, derr := config.Dir(); err == nil && derr == nil {
		err = addFile("update.log", filepath.Join(dir, "update.log"))
	}
	for _, pc := range cfg.Profiles {
		if err != nil {
			break
		}
		pp, perr := cfg.ProfilePaths(pc.Name)
		if perr != nil {
			continue
		}
		entries, _ := os.ReadDir(pp.Debug)
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			if fi, ierr := e.Info(); ierr != nil || !fi.Mode().IsRegular() || fi.Size() > diagMaxFile {
				continue
			}
			if err = addFile("debug/"+pc.Name+"/"+e.Name(), filepath.Join(pp.Debug, e.Name())); err != nil {
				break
			}
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
