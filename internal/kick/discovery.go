package kick

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	logger zerolog.Logger

	// DebugDir is the profile's debug dir for capture dumps; empty falls back
	// to <approot>/debug.
	DebugDir string

	// cfgMu guards cfg, swapped by SetConfig from the settings reload path.
	cfgMu sync.Mutex
	cfg   *config.Config

	mu   sync.Mutex
	last Diagnostic
}

// SetConfig swaps in a newer config for the next pass.
func (d *Discoverer) SetConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	d.cfgMu.Lock()
	d.cfg = cfg
	d.cfgMu.Unlock()
}

func (d *Discoverer) config() *config.Config {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	return d.cfg
}

// Diagnostic is a snapshot of the last Kick discovery pass, for the debug UI.
// Kick discovery is API+DOM shaped (no GQL capture), so it carries the campaigns
// endpoint status instead of Twitch's op/header telemetry.
type Diagnostic struct {
	At              time.Time `json:"at"`
	APIStatus       int       `json:"apiStatus"`
	CampaignsParsed int       `json:"campaignsParsed"`
	Enqueued        int       `json:"enqueued"`
	LastError       string    `json:"lastError,omitempty"`
}

// LastDiagnostic returns a snapshot of the most recent discovery pass.
func (d *Discoverer) LastDiagnostic() Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

func (d *Discoverer) record(diag Diagnostic) {
	d.mu.Lock()
	d.last = diag
	d.mu.Unlock()
}

// NewDiscoverer builds a Kick discoverer.
func NewDiscoverer(st *store.Store, pages *Pages, cfg *config.Config, logger zerolog.Logger) *Discoverer {
	return &Discoverer{store: st, pages: pages, cfg: cfg, logger: logger}
}

// RunOnce performs one full Kick discovery pass.
func (d *Discoverer) RunOnce(ctx context.Context) (discovery.Result, error) {
	diag := Diagnostic{At: time.Now()}
	res, err := d.runOnce(ctx, &diag)
	diag.CampaignsParsed = res.Campaigns
	diag.Enqueued = res.Enqueued
	if err != nil {
		diag.LastError = err.Error()
	}
	d.record(diag)
	return res, err
}

func (d *Discoverer) runOnce(ctx context.Context, diag *Diagnostic) (discovery.Result, error) {
	var res discovery.Result
	cfg := d.config()

	// Debug: seed the passive network-capture shim BEFORE any navigation so the
	// drops/inventory pages loaded below record their drops-related fetch/XHR
	// bodies. This is how a real Kick drop's progress endpoint gets captured
	// (dumped to kick-drops-network.json after the inventory read) so the parser
	// can be built from real data. Only when the capture toggle is on.
	if cfg.Logging.CaptureGQLBodies {
		if err := d.pages.SeedDropsCapture(ctx); err != nil {
			d.logger.Warn().Err(err).Msg("kick discovery: seed drops capture failed")
		}
	}

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
	if cfg.Logging.CaptureGQLBodies {
		d.dumpDebug("kick-campaigns.json", []byte(wrapped.Body))
	}
	diag.APIStatus = wrapped.Status
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

	// Inventory: progress parsing awaits live DOM shapes; dump it for that. The
	// rendered text (kick-inventory.txt) and full HTML (kick-inventory-raw.html)
	// carry any DOM-rendered drop progress; kick-drops-network.json carries the
	// raw API traffic the shim captured across the drops+inventory navigations -
	// together these are the Kick equivalent of Twitch's inventory-gql.json, kept
	// in their own files so the two platforms' dumps never collide.
	if _, err := d.pages.ReadInventory(ctx); err != nil {
		d.logger.Warn().Err(err).Msg("kick discovery: inventory read failed")
	} else if cfg.Logging.CaptureGQLBodies {
		d.dumpDebug("kick-inventory.txt", []byte(d.pages.InventoryText(ctx)))
		d.dumpDebug("kick-inventory-raw.html", []byte(d.pages.InventoryRawHTML(ctx)))
		if raw, err := d.pages.DrainDropsCapture(ctx); err != nil {
			d.logger.Warn().Err(err).Msg("kick discovery: drain drops capture failed")
		} else if raw != "" && raw != "[]" {
			d.dumpDebug("kick-drops-network.json", []byte(raw))
			d.logger.Info().Int("bytes", len(raw)).Msg("kick discovery: captured drops-related network traffic")
		}
	}

	enq, err := discovery.ReconcileQueue(d.store, cfg, domain.PlatformKick, d.logger)
	if err != nil {
		return res, err
	}
	res.Enqueued = enq
	d.logger.Info().Int("campaigns", res.Campaigns).Int("enqueued", res.Enqueued).
		Msg("kick discovery pass complete")
	return res, nil
}

// dumpDebug writes a raw body under the profile's debug dir for shape discovery.
func (d *Discoverer) dumpDebug(name string, body []byte) {
	ddir := discovery.DebugDirOr(d.DebugDir)
	if ddir == "" {
		return
	}
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
