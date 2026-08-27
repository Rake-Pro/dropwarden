package service

import (
	"encoding/json"
	"net/http"
	"time"
)

// NewHTTPHandler builds an http.Handler exposing the services as a small JSON
// API under /api/. The Wails app mounts it on the asset server, so the frontend
// talks to it with plain fetch - no generated bindings required, and it behaves
// identically under `go run`, `wails3 dev`, and a plain browser.
func NewHTTPHandler(b *Backend) http.Handler {
	campaigns := NewCampaignService(b)
	queue := NewQueueService(b)
	watch := NewWatcherService(b)
	history := NewHistoryService(b)
	settings := NewSettingsService(b)

	mux := http.NewServeMux()

	// Aggregate state for the dashboard/queue/history views in one round trip.
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		q, _ := queue.Get()
		claims, _ := history.Claims()
		stats, _ := history.Stats()
		camps, _ := campaigns.List()
		engineUp := b.Engine() != nil
		writeJSON(w, map[string]any{
			"connection": map[string]any{"chrome": engineUp, "loggedIn": engineUp},
			"status":     watch.Status(),
			"queue":      nonNilQueue(q),
			"claims":     claims,
			"stats":      stats,
			"campaigns":  camps,
			"config":     settings.Load(),
			"spark":      minutesSpark(history),
		})
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

	// Watcher actions.
	action := func(f func() error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := f(); err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		}
	}
	mux.HandleFunc("POST /api/watch/start", action(watch.Start))
	mux.HandleFunc("POST /api/watch/stop", action(watch.Stop))
	mux.HandleFunc("POST /api/watch/skip", action(watch.SkipCurrent))
	mux.HandleFunc("POST /api/watch/refresh", action(watch.ForceRefresh))

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
