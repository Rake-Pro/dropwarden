package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/logging"
	"github.com/rake-pro/dropwarden/internal/update"
)

// hostExitWait is the normal wait for the GUI host to quit. The host tears
// down every Chrome before exiting (10s per runtime at most), so a minute
// covers several profiles; hostExitGrace is the extra patience after that
// before giving up without touching any file.
const (
	hostExitWait  = 60 * time.Second
	hostExitGrace = 5 * time.Minute
)

// updateDownloadTimeout bounds the fallback download (job without Archive).
const updateDownloadTimeout = 15 * time.Minute

// updateApply is the hidden `dropwarden-cli update-apply <job.json>` command:
// the out-of-process half of the self-updater. The GUI host downloads and
// verifies the release, writes the job, starts this command and quits; this
// waits for the host to exit, swaps the installed files and starts the host
// again. Any failure is logged to update.log under the app root and the OLD
// host is relaunched, so an update never leaves the user with nothing running.
func updateApply(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dropwarden-cli update-apply <job.json>")
	}
	jobPath := args[0]
	defer os.Remove(jobPath)

	logger := updateLogger()
	job, err := update.ReadJob(jobPath)
	if err != nil {
		logger.Error().Err(err).Str("job", jobPath).Msg("update: read job")
		return err
	}
	logger = logger.With().Str("version", job.Version).Logger()
	logger.Info().Int("host_pid", job.HostPID).Str("host_exe", job.HostExe).Msg("update: waiting for dropwarden to exit")
	if !update.WaitExit(job.HostPID, hostExitWait) {
		logger.Warn().Int("host_pid", job.HostPID).Dur("grace", hostExitGrace).Msg("update: host still running; waiting longer")
		if !update.WaitExit(job.HostPID, hostExitGrace) {
			err := errors.New("host did not exit")
			logger.Error().Int("host_pid", job.HostPID).Msg("update: host did not exit; leaving the installation untouched")
			writeFailure(job, err, logger)
			if job.Archive != "" {
				os.Remove(job.Archive)
			}
			return err
		}
	}

	if err := installJob(job, logger); err != nil {
		logger.Error().Err(err).Msg("update: install failed; restarting the current version")
		writeFailure(job, err, logger)
		if job.Archive != "" {
			os.Remove(job.Archive)
		}
		if rerr := update.Relaunch(job.Relaunch, job.Args...); rerr != nil {
			logger.Error().Err(rerr).Msg("update: relaunch of the current version failed - start dropwarden manually")
		}
		return err
	}
	logger.Info().Str("relaunch", job.Relaunch).Msg("update: installed; starting the new version")
	if err := update.Relaunch(job.Relaunch, job.Args...); err != nil {
		logger.Error().Err(err).Msg("update: installed but could not start it - start dropwarden manually")
		return err
	}
	return nil
}

// writeFailure leaves update.FailureFile in the app data dir so the host
// reports the failure and does not auto-install this version again.
func writeFailure(job update.Job, cause error, logger zerolog.Logger) {
	dir, err := config.Dir()
	if err == nil {
		err = update.WriteFailure(dir, job.Version, cause)
	}
	if err != nil {
		logger.Error().Err(err).Msg("update: could not write the failure marker")
	}
}

// installJob applies the job's archive, downloading it from the feed first
// when the host did not hand one over.
func installJob(job update.Job, logger zerolog.Logger) error {
	archive := job.Archive
	if archive == "" {
		upd, err := update.New(update.Config{FeedURL: job.FeedURL, Current: job.Current})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), updateDownloadTimeout)
		defer cancel()
		rel, err := upd.Check(ctx)
		if err != nil {
			return err
		}
		if rel == nil {
			return fmt.Errorf("update: v%s is no longer newer than v%s; nothing to install", job.Version, job.Current)
		}
		if archive, err = upd.Download(ctx, rel); err != nil {
			return err
		}
	}
	target := job.HostExe
	if target == "" {
		target = job.Relaunch
	}
	logger.Info().Str("archive", archive).Str("target", target).Msg("update: applying")
	_, err := update.ApplyFor(archive, target)
	return err
}

// updateLogger writes to update.log under the app root (not dropwarden.log:
// the host may still hold that file while this starts, and the host's
// size-cap truncation must not race this process). Falls back to stderr.
func updateLogger() zerolog.Logger {
	opts := logging.Options{Level: "info"}
	if dir, err := config.Dir(); err == nil {
		opts.File = true
		opts.LogPath = filepath.Join(dir, "update.log")
		opts.MaxSizeMB = 1
	}
	logger, err := logging.Setup(opts)
	if err != nil {
		logger, _ = logging.Setup(logging.Options{Level: "info"})
	}
	return logger
}
