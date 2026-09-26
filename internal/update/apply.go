package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Apply installs the downloaded archive over the running installation and
// returns the path to launch for the new version. On darwin the whole .app
// bundle is swapped; on windows/linux each executable in the archive replaces
// its sibling next to the running binary (the running file is renamed aside
// first, which Windows allows even while it executes). The archive is removed
// on success.
func Apply(archivePath string) (relaunchPath string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locate executable: %w", err)
	}
	return ApplyFor(archivePath, exe)
}

// ApplyFor is Apply relative to a given installed executable instead of the
// running one: the out-of-process updater runs as dropwarden-cli update-apply
// and replaces the files that sit next to the host binary (job.HostExe).
func ApplyFor(archivePath, exe string) (relaunchPath string, err error) {
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("update: resolve executable: %w", err)
	}
	if runtime.GOOS == "darwin" {
		relaunchPath, err = applyDarwin(archivePath, exe)
	} else {
		relaunchPath, err = applyFlat(archivePath, exe)
	}
	if err != nil {
		return "", err
	}
	os.Remove(archivePath)
	log.Info().Str("relaunch", relaunchPath).Msg("update applied")
	return relaunchPath, nil
}

// applyDarwin swaps the whole dropwarden.app bundle. Extraction uses ditto so
// permissions, xattrs and the code signature survive intact.
func applyDarwin(archivePath, exe string) (string, error) {
	bundle := bundleRoot(exe)
	if bundle == "" {
		return "", fmt.Errorf("update: %s is not inside a .app bundle; cannot self-update", exe)
	}
	parent := filepath.Dir(bundle)
	stage, err := os.MkdirTemp(parent, "."+tempPrefix)
	if err != nil {
		return "", fmt.Errorf("update: stage dir: %w", err)
	}
	defer os.RemoveAll(stage)
	if out, err := exec.Command("ditto", "-x", "-k", archivePath, stage).CombinedOutput(); err != nil {
		return "", fmt.Errorf("update: ditto extract: %w: %s", err, out)
	}
	newBundle := filepath.Join(stage, filepath.Base(bundle))
	if _, err := os.Stat(newBundle); err != nil {
		return "", fmt.Errorf("update: archive missing %s: %w", filepath.Base(bundle), err)
	}
	old := bundle + ".old"
	os.RemoveAll(old)
	if err := os.Rename(bundle, old); err != nil {
		return "", fmt.Errorf("update: move current bundle aside: %w", err)
	}
	if err := os.Rename(newBundle, bundle); err != nil {
		// Roll back so the install is never left without a bundle.
		if rberr := os.Rename(old, bundle); rberr != nil {
			return "", fmt.Errorf("update: install new bundle: %w (ROLLBACK ALSO FAILED: %v; old bundle at %s)", err, rberr, old)
		}
		return "", fmt.Errorf("update: install new bundle: %w (rolled back)", err)
	}
	os.RemoveAll(old)
	return bundle, nil
}

// ProbeWritable checks that an update can be applied next to exe: it creates
// and removes a stage-like temp dir where Apply would stage (the exe's dir, or
// the .app bundle's parent on darwin).
func ProbeWritable(exe string) error {
	dir := filepath.Dir(exe)
	if runtime.GOOS == "darwin" {
		bundle := bundleRoot(exe)
		if bundle == "" {
			return fmt.Errorf("%s is not inside a .app bundle", exe)
		}
		dir = filepath.Dir(bundle)
	}
	probe, err := os.MkdirTemp(dir, "."+tempPrefix+"probe-*")
	if err != nil {
		return err
	}
	return os.Remove(probe)
}

// bundleRoot walks up from exe to the enclosing .app directory ("" if none).
func bundleRoot(exe string) string {
	for dir := filepath.Dir(exe); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
	}
	return ""
}

// applyFlat replaces executables next to the running binary from a .zip or
// .tar.gz archive (windows/linux layout: loose binaries at the archive root).
func applyFlat(archivePath, exe string) (string, error) {
	destDir := filepath.Dir(exe)
	stage, err := os.MkdirTemp(destDir, "."+tempPrefix)
	if err != nil {
		return "", fmt.Errorf("update: stage dir: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := extract(archivePath, stage); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", fmt.Errorf("update: read stage: %w", err)
	}
	installed := 0
	var movedAside []string // dst paths whose original now sits at dst+".old"
	rollback := func(cause error) error {
		for _, dst := range movedAside {
			if rberr := renameRetry(dst+".old", dst); rberr != nil {
				log.Error().Err(rberr).Str("file", dst).Msg("update: rollback failed; original left at .old")
				return fmt.Errorf("%w (ROLLBACK ALSO FAILED for %s: %v)", cause, dst, rberr)
			}
		}
		log.Warn().Err(cause).Int("restored", len(movedAside)).Msg("update: apply failed, rolled back to the previous files")
		return fmt.Errorf("%w (rolled back)", cause)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(stage, e.Name())
		dst := filepath.Join(destDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			old := dst + ".old"
			os.Remove(old)
			if err := os.Rename(dst, old); err != nil {
				return "", rollback(fmt.Errorf("update: move %s aside: %w", dst, err))
			}
			movedAside = append(movedAside, dst)
		}
		if err := renameRetry(src, dst); err != nil {
			return "", rollback(fmt.Errorf("update: install %s: %w", dst, err))
		}
		installed++
	}
	if installed == 0 {
		return "", fmt.Errorf("update: archive %s contained no files", archivePath)
	}
	return exe, nil
}

// renameAttempts and renameRetryDelay cover transient sharing violations on
// Windows, where antivirus often holds a freshly written binary for a moment.
const renameAttempts = 5

var renameRetryDelay = 400 * time.Millisecond

func renameRetry(src, dst string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if i > 0 {
			time.Sleep(renameRetryDelay)
		}
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
	}
	return err
}

// extract unpacks a .zip or .tar.gz into dir, flattening to basenames and
// rejecting anything that is not a regular file.
func extract(archivePath, dir string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZip(archivePath, dir)
	}
	if strings.HasSuffix(archivePath, ".tar.gz") {
		return extractTarGz(archivePath, dir)
	}
	return fmt.Errorf("update: unsupported archive %s", archivePath)
}

func extractZip(archivePath, dir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("update: open zip: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("update: zip entry %s: %w", f.Name, err)
		}
		err = writeFile(filepath.Join(dir, filepath.Base(f.Name)), rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archivePath, dir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("update: open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("update: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("update: tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := writeFile(filepath.Join(dir, filepath.Base(hdr.Name)), tr, os.FileMode(hdr.Mode)); err != nil {
			return err
		}
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o100)
	if err != nil {
		return fmt.Errorf("update: create %s: %w", path, err)
	}
	_, err = io.Copy(out, r)
	cerr := out.Close()
	if err != nil || cerr != nil {
		return fmt.Errorf("update: write %s: copy=%v close=%v", path, err, cerr)
	}
	return nil
}

// SelfLaunchPath returns what Relaunch should start to re-run the current
// app: the enclosing .app bundle on darwin, else the executable itself.
func SelfLaunchPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("update: resolve executable: %w", err)
	}
	if runtime.GOOS == "darwin" {
		if bundle := bundleRoot(exe); bundle != "" {
			return bundle, nil
		}
	}
	return exe, nil
}

// Relaunch starts the app detached from this process, forwarding args (on
// darwin via `open --args`). The caller is expected to quit right after.
func Relaunch(path string, args ...string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		openArgs := []string{"-n", path}
		if len(args) > 0 {
			openArgs = append(append(openArgs, "--args"), args...)
		}
		cmd = exec.Command("open", openArgs...)
	} else {
		cmd = exec.Command(path, args...)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update: relaunch %s: %w", path, err)
	}
	return cmd.Process.Release()
}
