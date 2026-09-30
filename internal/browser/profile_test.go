package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
)

func TestMarkCleanExitRewritesCrashedProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, preferencesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"profile":{"exit_type":"Crashed","exited_cleanly":false,"name":"Person 1"},"browser":{"has_seen_welcome_page":true}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	markCleanExit(dir, zerolog.Nop())

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefs map[string]any
	if err := json.Unmarshal(raw, &prefs); err != nil {
		t.Fatalf("rewritten preferences are not json: %v\n%s", err, raw)
	}
	profile := prefs["profile"].(map[string]any)
	if profile["exit_type"] != "Normal" || profile["exited_cleanly"] != true {
		t.Errorf("profile not marked clean: %v", profile)
	}
	if profile["name"] != "Person 1" {
		t.Errorf("sibling profile key clobbered: %v", profile)
	}
	if b, _ := prefs["browser"].(map[string]any); b["has_seen_welcome_page"] != true {
		t.Errorf("unrelated top-level key clobbered: %v", prefs)
	}
}

func TestMarkCleanExitLeavesMissingAndBrokenFilesAlone(t *testing.T) {
	dir := t.TempDir()
	markCleanExit(dir, zerolog.Nop()) // no Preferences yet: must not create one
	if _, err := os.Stat(filepath.Join(dir, preferencesFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no preferences file, got err=%v", err)
	}

	path := filepath.Join(dir, preferencesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	markCleanExit(dir, zerolog.Nop())
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not json" {
		t.Errorf("unparseable preferences were rewritten: %q", raw)
	}
}
