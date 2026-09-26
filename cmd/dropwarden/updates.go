//go:build windows || darwin || linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/update"
)

// updateTray binds the self-updater (service.UpdateController, which holds
// all the logic and the /api/update state) to the tray: it locates the
// dropwarden-cli helper, starts it detached for a handoff, and mirrors the
// controller state onto the "Check for Updates" tray item.
type updateTray struct {
	*service.UpdateController
	item *application.MenuItem
}

func newUpdateController(ctx context.Context, cfg func() *config.Config, backend *service.Backend) *updateTray {
	t := &updateTray{}
	opt := service.UpdateOptions{
		Ctx:     ctx,
		Config:  cfg,
		Backend: backend,
		Changed: t.retitle,
		StartHelper: func(helper, jobPath string) error {
			cmd := exec.Command(helper, "update-apply", jobPath)
			cmd.SysProcAttr = detachedProcAttr()
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		},
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			opt.HostExe = exe
			name := "dropwarden-cli"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			helper := filepath.Join(filepath.Dir(exe), name)
			if _, err := os.Stat(helper); err == nil {
				opt.Helper = helper
			} else {
				log.Debug().Str("helper", helper).Msg("update: no dropwarden-cli helper, installs stay in-process")
			}
		}
	}
	if p, err := update.SelfLaunchPath(); err == nil {
		opt.Relaunch = p
	}
	t.UpdateController = service.NewUpdateController(opt)
	return t
}

// attach wires the tray item. Called once while the tray is built. A click
// opens the dashboard and asks it for the update dialog (or runs a check);
// the tray never installs silently.
func (t *updateTray) attach(item *application.MenuItem) {
	t.item = item
	item.OnClick(func(*application.Context) { go t.TrayClick() })
	t.retitle()
}

// retitle mirrors the controller state onto the tray item.
func (t *updateTray) retitle() {
	if t.item == nil || t.UpdateController == nil {
		return
	}
	title, enabled := t.TrayLabel()
	t.item.SetLabel(title)
	t.item.SetEnabled(enabled)
}
