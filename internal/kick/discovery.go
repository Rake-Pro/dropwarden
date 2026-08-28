package kick

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

// Discoverer runs one Kick discovery pass: warm the drops page, fetch the
// campaigns API from page context (cookies + Cloudflare clearance ride along),
// reconcile the shared queue for the kick platform, and read the inventory
// page. It satisfies the engine's Discoverer interface.
type Discoverer struct {
	store  *store.Store
	pages  *Pages
	cfg    *config.Config
	logger zerolog.Logger
}

// NewDiscoverer builds a Kick discoverer.
func NewDiscoverer(st *store.Store, pages *Pages, cfg *config.Config, logger zerolog.Logger) *Discoverer {
	return &Discoverer{store: st, pages: pages, cfg: cfg, logger: logger}
}

// RunOnce performs one full Kick discovery pass.
func (d *Discoverer) RunOnce(ctx context.Context) (discovery.Result, error) {
	var res discovery.Result

	// Warm the drops page first: it establishes the authenticated, Cloudflare-
	// cleared context the API fetch inherits.
	if err := d.pages.ctrl.Session().Navigate(ctx, URLDrops); err != nil {
		return res, fmt.Errorf("kick: open drops page: %w", err)
	}
	d.pages.settle(ctx)

	var raw string
	if err := d.pages.ctrl.Session().Eval(ctx, jsFetchCampaigns, &raw); err != nil {
		return res, fmt.Errorf("kick: fetch campaigns: %w", err)
	}
	var wrapped struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return res, fmt.Errorf("kick: decode campaigns wrapper (%.120s): %w", raw, err)
	}
	if d.cfg.Logging.CaptureGQLBodies {
		d.dumpDebug("kick-campaigns.json", []byte(wrapped.Body))
	}
	if wrapped.Status != 200 {
		// The endpoint is unofficial; a non-200 is diagnosis material, not fatal.
		d.logger.Warn().Int("status", wrapped.Status).Str("body_sample", clip(wrapped.Body, 300)).
			Msg("kick discovery: campaigns endpoint did not return 200")
	} else {
		campaigns, err := ParseCampaigns([]byte(wrapped.Body))
		if err != nil {
			d.logger.Warn().Err(err).Str("body_sample", clip(wrapped.Body, 300)).
				Msg("kick discovery: campaigns parse failed - dump the body and fix the shape")
		}
		for _, c := range campaigns {
			if err := d.store.UpsertCampaign(c); err != nil {
				return res, err
			}
			res.Campaigns++
		}
		d.logger.Info().Int("campaigns_parsed", len(campaigns)).Msg("kick discovery: campaigns")
	}

	// Inventory: progress parsing awaits live DOM shapes; dump text for that.
	if _, err := d.pages.ReadInventory(ctx); err != nil {
		d.logger.Warn().Err(err).Msg("kick discovery: inventory read failed")
	} else if d.cfg.Logging.CaptureGQLBodies {
		d.dumpDebug("kick-inventory.txt", []byte(d.pages.InventoryText(ctx)))
	}

	enq, err := discovery.ReconcileQueue(d.store, d.cfg, domain.PlatformKick, d.logger)
	if err != nil {
		return res, err
	}
	res.Enqueued = enq
	d.logger.Info().Int("campaigns", res.Campaigns).Int("enqueued", res.Enqueued).
		Msg("kick discovery pass complete")
	return res, nil
}

// dumpDebug writes a raw body under <approot>/debug for shape discovery.
func (d *Discoverer) dumpDebug(name string, body []byte) {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	ddir := filepath.Join(dir, "debug")
	if err := os.MkdirAll(ddir, 0o700); err != nil {
		return
	}
	path := filepath.Join(ddir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		d.logger.Warn().Err(err).Msg("kick discovery: write debug dump failed")
		return
	}
	d.logger.Info().Str("file", path).Int("bytes", len(body)).Msg("kick discovery: wrote debug dump")
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
