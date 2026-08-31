package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/kick"
	"github.com/rake-pro/dropwarden/internal/logging"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// loginCheckTimeout bounds one on-demand login probe: it navigates the watcher
// Chrome to the platform home page and evaluates the logged-in selector.
const loginCheckTimeout = 30 * time.Second

// NewHTTPHandler builds an http.Handler exposing the services as a small JSON
// API under /api/. The Wails app mounts it on the asset server, so the frontend
// talks to it with plain fetch - no generated bindings required, and it behaves
// identically under `go run`, `wails3 dev`, and a plain browser.
func NewHTTPHandler(b *Backend) http.Handler {
	queue := NewQueueService(b)
	history := NewHistoryService(b)
	settings := NewSettingsService(b)
	watch := NewWatcherService(b)

	mux := http.NewServeMux()

	// Aggregate state for the dashboard/queue/history views in one round trip:
	// the shared config plus one entry per profile carrying that profile's
	// platforms, queue, claims, stats, and sparkline. Platforms stay a uniform
	// array (twitch first) so the frontend renders every platform the same way.
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"config":   settings.Load(),
			"profiles": profileStates(b, queue, history),
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

	// Engine actions, uniform across profiles and platforms:
	// /api/engine/{profile}/{platform}/{action}.
	mux.HandleFunc("POST /api/engine/{profile}/{platform}/{action}", func(w http.ResponseWriter, r *http.Request) {
		eff, err := b.config().Effective(r.PathValue("profile"))
		if err != nil {
			writeErr(w, fmt.Errorf("unknown profile %q", r.PathValue("profile")))
			return
		}
		p := domain.Platform(r.PathValue("platform"))
		if p != domain.PlatformTwitch && p != domain.PlatformKick {
			writeErr(w, fmt.Errorf("unknown platform %q", r.PathValue("platform")))
			return
		}
		// start/login/stop drive the runtime lifecycle, so they must work with no
		// engine up: start and login LAUNCH Chrome, and stop tears it down. Only
		// skip and refresh need a live engine; they keep the richer down-reason.
		enabled := eff.Enabled && (p != domain.PlatformKick || eff.KickEnabled)
		needsEngine := func() bool {
			if e := b.EngineFor(eff.Name, p); e != nil {
				return true
			}
			writeErr(w, engineDownError(b, eff, p))
			return false
		}
		// Routed through the service rather than the engine handle so "start"
		// also answers the needs-login prompt (see WatcherService.Start).
		var actErr error
		switch r.PathValue("action") {
		case "start":
			if !enabled {
				writeErr(w, engineDownError(b, eff, p))
				return
			}
			actErr = watch.Start(eff.Name, p)
		case "login":
			if !enabled {
				writeErr(w, engineDownError(b, eff, p))
				return
			}
			actErr = watch.Login(eff.Name, p)
		case "stop":
			actErr = watch.Stop(eff.Name, p)
		case "skip":
			if !needsEngine() {
				return
			}
			actErr = watch.SkipCurrent(eff.Name, p)
		case "refresh":
			if !needsEngine() {
				return
			}
			actErr = watch.ForceRefresh(eff.Name, p)
		default:
			writeErr(w, fmt.Errorf("unknown action %q", r.PathValue("action")))
			return
		}
		if actErr != nil {
			writeErr(w, actErr)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// Queue actions. Each names the profile whose queue it edits; an empty
	// profile means the first one (the single-profile case).
	mux.HandleFunc("POST /api/queue/order", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Profile string   `json:"profile"`
			IDs     []string `json:"ids"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := queue.SetOrder(body.Profile, body.IDs); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/queue/enabled", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Profile string `json:"profile"`
			DropID  string `json:"dropID"`
			On      bool   `json:"on"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := queue.SetEnabled(body.Profile, body.DropID, body.On); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Priority mode is a root setting shared by every profile.
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

	// Profile management. Every one of these takes effect immediately: the
	// settings commit reconciles the running runtimes against the new config,
	// so an added, enabled, disabled, or removed profile starts or stops its
	// Chrome without an app restart. Launches take seconds, so the response
	// says only that the change was accepted; the UI polls /api/state to watch
	// the runtime appear. No rename by design.
	mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := settings.AddProfile(body.Name, body.DisplayName); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/profiles/enabled", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			On   bool   `json:"on"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := settings.SetProfileEnabled(body.Name, body.On); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// Removing a profile stops its runtimes and closes its store but NEVER
	// deletes its data directory; the response returns that path so the UI can
	// tell the user where the leftovers live.
	mux.HandleFunc("POST /api/profiles/remove", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &body) {
			return
		}
		dir, err := settings.RemoveProfile(body.Name)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "dataDir": dir})
	})

	// On-demand login probe for one (profile, platform) session: true, false,
	// or null when the answer is unknowable (no session up, or a platform with
	// no probe). Never called from the state poll - it drives a real navigation
	// in that Chrome.
	mux.HandleFunc("POST /api/profiles/login-check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Profile  string `json:"profile"`
			Platform string `json:"platform"`
		}
		if !decode(w, r, &body) {
			return
		}
		plat := domain.Platform(body.Platform)
		if plat != domain.PlatformTwitch && plat != domain.PlatformKick {
			writeErr(w, fmt.Errorf("unknown platform %q", body.Platform))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), loginCheckTimeout)
		defer cancel()
		in, detail, err := watch.LoginCheck(ctx, body.Profile, plat)
		if err != nil {
			writeErr(w, err)
			return
		}
		out := map[string]any{"loggedIn": in}
		if detail != "" {
			// Why the answer is null (probe status, drifted body keys) - shown
			// by the UI so endpoint drift is fixable without log spelunking.
			out["detail"] = detail
		}
		writeJSON(w, out)
	})

	// GET /api/profiles lists every profile's raw editable overlay for the
	// Profiles settings pane (distinct from POST /api/profiles, which adds one).
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, settings.Profiles())
	})
	mux.HandleFunc("POST /api/profiles/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name         string   `json:"name"`
			DisplayName  string   `json:"displayName"`
			KickEnabled  bool     `json:"kickEnabled"`
			Games        []string `json:"games"`
			InheritGames bool     `json:"inheritGames"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := settings.UpdateProfile(body.Name, body.DisplayName, body.KickEnabled, body.Games, body.InheritGames); err != nil {
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
// days, derived from one profile's session history.
func minutesSpark(h *HistoryService, profile string) []int {
	sessions, err := h.Sessions(profile, 0)
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

// profileStates builds the per-profile state array for /api/state, in config
// order. A profile this process did not open (disabled at start, or added
// since) still appears, reporting no engines and empty data instead of
// vanishing from the UI.
func profileStates(b *Backend, queue *QueueService, history *HistoryService) []map[string]any {
	cfg := b.config()
	out := make([]map[string]any, 0, len(cfg.Profiles))
	for _, pc := range cfg.Profiles {
		eff, err := cfg.Effective(pc.Name)
		if err != nil {
			continue
		}
		pb := b.Profile(eff.Name)
		entry := map[string]any{
			"name": eff.Name, "displayName": eff.DisplayName, "enabled": eff.Enabled,
			"platforms": platformStates(b, eff, pb),
			"queue":     []QueueItemDTO{},
			"claims":    []ClaimDTO{},
			"stats":     StatsDTO{OutcomeCounts: map[string]int{}, ByPlatform: map[string]PlatformTotalsDTO{}},
			"spark":     []int{},
		}
		if pb != nil {
			q, _ := queue.Get(eff.Name)
			entry["queue"] = nonNilQueue(q)
			if claims, err := history.Claims(eff.Name); err == nil {
				entry["claims"] = claims
			}
			if stats, err := history.Stats(eff.Name); err == nil {
				entry["stats"] = stats
			}
			entry["spark"] = minutesSpark(history, eff.Name)
		}
		out = append(out, entry)
	}
	return out
}

// platformStates builds one profile's uniform per-platform runtime array,
// twitch first. Every enable/disable is live now, so the pair (enabled, up)
// says everything: enabled + up = running; enabled + not up = starting or
// failed to start; not enabled = off. needsLogin marks a runtime that came up
// against a fresh Chrome profile and is paused until the user logs in.
func platformStates(b *Backend, eff *config.ProfileEffective, pb *ProfileBackend) []map[string]any {
	entry := func(p domain.Platform, enabled bool) map[string]any {
		m := map[string]any{"platform": string(p), "enabled": enabled, "up": false, "needsLogin": false, "wanted": false, "status": nil}
		if pb != nil {
			up := false
			if e := pb.Engine(p); e != nil {
				up = true
				m["up"] = true
				m["status"] = e.Status()
			}
			// While up the attached flag is authoritative (login mode vs
			// watching); while down it is always false, so read the on-disk
			// first-run state so the dashboard still knows to offer Log in.
			if up {
				m["needsLogin"] = pb.NeedsLogin(p)
			} else if enabled {
				m["needsLogin"] = b.needsLoginDown(eff.Name, p)
				// wanted only matters while down: a live run intent with no
				// runtime up means Chrome is launching or its launch failed,
				// which must not render as a plain user-chosen "stopped".
				m["wanted"] = b.wantsRun(eff.Name, p)
			}
		}
		return m
	}
	return []map[string]any{
		entry(domain.PlatformTwitch, eff.Enabled),
		entry(domain.PlatformKick, eff.Enabled && eff.KickEnabled),
	}
}

// engineDownError explains WHY a (profile, platform) engine is unavailable,
// keeping the disabled-vs-not-running distinction honest per profile.
func engineDownError(b *Backend, eff *config.ProfileEffective, p domain.Platform) error {
	if !eff.Enabled {
		return fmt.Errorf("profile %q is disabled - enable it in Settings", eff.Name)
	}
	if p == domain.PlatformKick && !eff.KickEnabled {
		return fmt.Errorf("kick is disabled for %s - enable it in Settings > Platforms", eff.DisplayName)
	}
	if b.Profile(eff.Name) == nil {
		return fmt.Errorf("profile %q has no store open yet - it is still starting, or its store failed to open (see the Debugging view)", eff.Name)
	}
	return fmt.Errorf("%s runtime is not running for %s (starting Chrome, or it failed to launch - see the Debugging view)", p, eff.DisplayName)
}

// buildDebug assembles the debug snapshot for the Debugging pane: one section
// per profile (and per platform within it) plus the shared log tail, with paths
// listed per profile.
func buildDebug(b *Backend) map[string]any {
	cfg := b.config()
	dir, _ := config.Dir()
	logPath, _ := config.LogPath()

	profiles := map[string]any{}
	profilePaths := map[string]any{}
	for _, pc := range cfg.Profiles {
		eff, err := cfg.Effective(pc.Name)
		if err != nil {
			continue
		}
		pb := b.Profile(eff.Name)
		profiles[eff.Name] = map[string]any{
			"displayName": eff.DisplayName,
			"enabled":     eff.Enabled,
			"twitch":      platformDebug(pb, eff.Enabled, domain.PlatformTwitch),
			"kick":        platformDebug(pb, eff.Enabled && eff.KickEnabled, domain.PlatformKick),
		}
		pp, err := cfg.ProfilePaths(eff.Name)
		if err != nil {
			continue
		}
		chromeProfile, kickChromeProfile := pp.ChromeProfile, pp.ChromeProfileKick
		// The browser profile_dir overrides are single-instance settings and
		// apply to the main profile only (see app.chromeDirs).
		if eff.Name == config.MainProfileName {
			if d, err := cfg.ProfileDir(); err == nil {
				chromeProfile = d
			}
			if d, err := cfg.KickProfileDir(); err == nil {
				kickChromeProfile = d
			}
		}
		profilePaths[eff.Name] = map[string]string{
			"database": pp.DB, "chromeProfile": chromeProfile,
			"kickChromeProfile": kickChromeProfile, "debugDir": pp.Debug,
		}
	}

	return map[string]any{
		"profiles": profiles,
		"logTail":  logging.RecentLines(120),
		"paths": map[string]any{
			"appRoot": dir, "config": b.ConfigPath, "log": logPath,
			"profiles": profilePaths,
		},
	}
}

// platformDebug renders one (profile, platform) debug section.
func platformDebug(pb *ProfileBackend, enabled bool, p domain.Platform) map[string]any {
	m := map[string]any{"enabled": enabled, "up": false, "status": nil, "eligibility": map[string]int{}, "lastDiscovery": nil}
	if pb == nil {
		return m
	}
	if eng := pb.Engine(p); eng != nil {
		m["up"] = true
		m["status"] = eng.Status()
		m["eligibility"] = eng.LastEligibility()
	}
	if diag := lastDiscovery(pb.Discoverer(p)); diag != nil {
		m["lastDiscovery"] = diag
	}
	return m
}

// lastDiscovery returns a discoverer's last-pass diagnostic, or nil when no
// pass has run. The two platforms report different fields, so the debug view
// takes it as opaque JSON.
func lastDiscovery(d watcher.Discoverer) any {
	switch v := d.(type) {
	case *discovery.Discoverer:
		if diag := v.LastDiagnostic(); !diag.At.IsZero() {
			return diag
		}
	case *kick.Discoverer:
		if diag := v.LastDiagnostic(); !diag.At.IsZero() {
			return diag
		}
	}
	return nil
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
