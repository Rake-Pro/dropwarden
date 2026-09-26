package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Job is the hand-off from the GUI host to the out-of-process updater
// (dropwarden-cli update-apply <job.json>): everything the updater needs to
// wait for the host to exit, install the release and start the host again.
// The host normally downloads and verifies the archive itself before it
// quits and passes it in Archive, so a network failure never leaves the host
// gone; FeedURL is the fallback when Archive is empty. It lives in a small
// JSON file the updater deletes when done.
type Job struct {
	Version  string   `json:"version"`  // release being installed, e.g. "1.4.0"
	FeedURL  string   `json:"feed_url"` // manifest URL, used only when Archive is empty
	Current  string   `json:"current"`  // host version, for the newer-than check
	HostPID  int      `json:"host_pid"` // process to wait for before touching files
	Relaunch string   `json:"relaunch"` // SelfLaunchPath of the host (exe, or .app on darwin)
	HostExe  string   `json:"host_exe"` // the host binary; the install happens next to it
	Args     []string `json:"args"`     // host args to forward on relaunch
	Archive  string   `json:"archive"`  // verified archive the host already downloaded
}

// WriteJob stores j as JSON in a fresh temp file and returns its path.
func WriteJob(j Job) (string, error) {
	f, err := os.CreateTemp("", tempPrefix+"*.json")
	if err != nil {
		return "", fmt.Errorf("update: job file: %w", err)
	}
	if err := json.NewEncoder(f).Encode(j); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("update: write job: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("update: write job: %w", err)
	}
	return f.Name(), nil
}

// ReadJob loads a job file written by WriteJob.
func ReadJob(path string) (Job, error) {
	var j Job
	b, err := os.ReadFile(path)
	if err != nil {
		return j, fmt.Errorf("update: read job: %w", err)
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return j, fmt.Errorf("update: parse job %s: %w", filepath.Base(path), err)
	}
	if j.Version == "" || j.Relaunch == "" || (j.Archive == "" && j.FeedURL == "") {
		return j, fmt.Errorf("update: job %s is incomplete", filepath.Base(path))
	}
	return j, nil
}

// FailureFile is the marker update-apply leaves in the app data dir when an
// install fails, so the relaunched host can report it and stop retrying the
// same version automatically.
const FailureFile = "update-failed.json"

// Failure is the content of FailureFile.
type Failure struct {
	Version string    `json:"version"`
	Error   string    `json:"error"`
	Time    time.Time `json:"time"`
}

// WriteFailure records a failed install of version in dir.
func WriteFailure(dir, version string, cause error) error {
	b, err := json.Marshal(Failure{Version: version, Error: cause.Error(), Time: time.Now()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("update: failure marker dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, FailureFile), b, 0o600)
}

// TakeFailure reads and deletes the failure marker in dir. It returns nil
// when there is none (or it is unreadable, which is also deleted).
func TakeFailure(dir string) *Failure {
	path := filepath.Join(dir, FailureFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	os.Remove(path)
	var f Failure
	if json.Unmarshal(b, &f) != nil || f.Version == "" {
		return nil
	}
	return &f
}

// CleanupOld removes what a previous update left next to the running
// binary: "dropwarden*.old" siblings (the replaced executables) and abandoned
// ".dropwarden-update-*" stage directories. On darwin it also drops the
// "<App>.app.old" bundle beside the current one. Best-effort: files still in
// use are skipped and retried on the next start.
func CleanupOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return
	}
	cleanDir(filepath.Dir(exe), true)
	if bundle := bundleRoot(exe); bundle != "" {
		os.RemoveAll(bundle + ".old")
		// The bundle's parent (e.g. /Applications) is shared with other apps:
		// only our own stage directories are touched there.
		cleanDir(filepath.Dir(bundle), false)
	}
}

// cleanDir removes abandoned stage directories in dir and, when oldFiles is
// set, the "dropwarden*.old" executables a flat apply moved aside. Only our
// own names are matched, so an install dir shared with other programs (a
// Linux /usr/local/bin) keeps their files.
func cleanDir(dir string, oldFiles bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case oldFiles && !e.IsDir() && strings.HasPrefix(name, "dropwarden") && filepath.Ext(name) == ".old":
			os.Remove(filepath.Join(dir, name))
		case e.IsDir() && strings.HasPrefix(name, "."+tempPrefix) && len(name) > len("."+tempPrefix):
			os.RemoveAll(filepath.Join(dir, name))
		}
	}
}
