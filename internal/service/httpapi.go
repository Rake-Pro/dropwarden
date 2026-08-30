package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/logging"
)

// NewHTTPHandler builds an http.Handler exposing the services as a small JSON
// API under /api/. The Wails app mounts it on the asset server, so the frontend
// talks to it with plain fetch - no generated bindings required, and it behaves
// identically under `go run`, `wails3 dev`, and a plain browser.
func NewHTTPHandler(b *Backend) http.Handler {
	queue := NewQueueService(b)
	history := NewHistoryService(b)
	settings := NewSettingsService(b)

	mux := http.NewServeMux()

	// Aggregate state for the dashboard/queue/history views in one round trip.
	// Platforms are a uniform array (twitch first) so the frontend renders every
	// platform the same way instead of special-casing a bolted-on second one.
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		q, _ := queue.Get()
		claims, _ := history.Claims()
		stats, _ := history.Stats()
		writeJSON(w, map[string]any{
			"platforms": platformStates(b),
			"queue":     nonNilQueue(q),
			"claims":    claims,
			"stats":     stats,
			"config":    settings.Load(),
			"spark":     minutesSpark(history),
		})
	})

	mux.HandleFunc("GET /api/debug", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildDebug(b))
	})

	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, settings.Load())
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var dto ConfigDTO
		if !decode(w, r, &dto) {
			return
		}
		if err := settings.Save(dto); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// Engine actions, uniform across platforms: /api/engine/{platform}/{action}.
	mux.HandleFunc("POST /api/engine/{platform}/{action}", func(w http.ResponseWriter, r *http.Request) {
		p := domain.Platform(r.PathValue("platform"))
		if p != domain.PlatformTwitch && p != domain.PlatformKick {
			writeErr(w, fmt.Errorf("unknown platform %q", r.PathValue("platform")))
			return
		}
		e := b.EngineFor(p)
		if e == nil {
			if p == domain.PlatformKick && !b.config().Kick.Enabled {
				writeErr(w, fmt.Errorf("kick is disabled — enable it in Settings › Platforms and restart"))
			} else {
				writeErr(w, fmt.Errorf("%s runtime is not running", p))
			}
			return
		}
		switch r.PathValue("action") {
		case "start":
			e.Start()
		case "stop":
			e.Stop()
		case "skip":
			e.SkipCurrent()
		case "refresh":
			e.RefreshNow()
		default:
			writeErr(w, fmt.Errorf("unknown action %q", r.PathValue("action")))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// Queue actions.
	mux.HandleFunc("POST /api/queue/order", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := queue.SetOrder(body.IDs); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/queue/enabled", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DropID string `json:"dropID"`
			On     bool   `json:"on"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := queue.SetEnabled(body.DropID, body.On); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/queue/priority", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := queue.SetPriorityMode(body.Mode); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	return mux
}

// nonNilQueue ensures the JSON encodes [] not null for an empty queue.
func nonNilQueue(q []QueueItemDTO) []QueueItemDTO {
	if q == nil {
		return []QueueItemDTO{}
	}
	return q
}

// minutesSpark returns a coarse per-day watch-minutes series for the last 14
// days, derived from session history.
func minutesSpark(h *HistoryService) []int {
	sessions, err := h.Sessions(0)
	if err != nil {
		return []int{}
	}
	const days = 14
	buckets := make([]int, days)
	now := time.Now()
	max := 1
	for _, s := range sessions {
		if s.StartedAt.IsZero() {
			continue
		}
		d := int(now.Sub(s.StartedAt).Hours() / 24)
		if d < 0 || d >= days {
			continue
		}
		idx := days - 1 - d
		buckets[idx] += s.Minutes
		if buckets[idx] > max {
			max = buckets[idx]
		}
	}
	// Scale to 0..100 for the sparkline.
	out := make([]int, days)
	for i, v := range buckets {
		out[i] = v * 100 / max
	}
	return out
}

// platformStates builds the uniform per-platform runtime array for /api/state,
// twitch first. A platform that is enabled but has no engine attached is either
// still starting or failed to launch; the frontend tells those apart by whether
// the rest of the runtime is up.
func platformStates(b *Backend) []map[string]any {
	entry := func(p domain.Platform, enabled, pendingRestart bool) map[string]any {
		m := map[string]any{"platform": string(p), "enabled": enabled, "pendingRestart": pendingRestart, "up": false, "status": nil}
		if e := b.EngineFor(p); e != nil {
			m["up"] = true
			m["status"] = e.Status()
		}
		return m
	}
	kickEnabled := b.config().Kick.Enabled
	return []map[string]any{
		entry(domain.PlatformTwitch, true, false),
		// pendingRestart: the saved toggle differs from what this process
		// started with — the runtime follows on the next app restart.
		entry(domain.PlatformKick, kickEnabled, kickEnabled != b.startupKickEnabled),
	}
}

// buildDebug assembles the debug snapshot for the Debugging pane, one section
// per platform plus the shared log tail and paths.
func buildDebug(b *Backend) map[string]any {
	dir, _ := config.Dir()
	dbPath, _ := config.DBPath()
	logPath, _ := config.LogPath()
	profile, _ := b.config().ProfileDir()
	kickProfile, _ := b.config().KickProfileDir()
	debugDir := ""
	if dir != "" {
		debugDir = filepath.Join(dir, "debug")
	}

	twitch := map[string]any{"enabled": true, "up": b.Engine() != nil, "status": nil, "eligibility": map[string]int{}, "lastDiscovery": nil}
	if eng := b.Engine(); eng != nil {
		twitch["status"] = eng.Status()
		twitch["eligibility"] = eng.LastEligibility()
	}
	if d := b.Discoverer(); d != nil {
		if diag := d.LastDiagnostic(); !diag.At.IsZero() {
			twitch["lastDiscovery"] = diag
		}
	}

	kick := map[string]any{"enabled": b.config().Kick.Enabled, "up": b.KickEngine() != nil, "status": nil, "eligibility": map[string]int{}, "lastDiscovery": nil}
	if eng := b.KickEngine(); eng != nil {
		kick["status"] = eng.Status()
		kick["eligibility"] = eng.LastEligibility()
	}
	if d := b.KickDiscoverer(); d != nil {
		if diag := d.LastDiagnostic(); !diag.At.IsZero() {
			kick["lastDiscovery"] = diag
		}
	}

	return map[string]any{
		"platforms": map[string]any{"twitch": twitch, "kick": kick},
		"logTail":   logging.RecentLines(120),
		"paths": map[string]string{
			"appRoot": dir, "config": b.ConfigPath, "database": dbPath,
			"log": logPath, "chromeProfile": profile, "kickChromeProfile": kickProfile, "debugDir": debugDir,
		},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, err)
		return false
	}
	return true
}
