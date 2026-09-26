package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
)

const (
	CacheTTL        = 10 * time.Minute
	RefreshMinGap   = 60 * time.Second
	searchPerSecond = 2
	searchCacheMax  = 500
	minQuery        = 2
	maxQuery        = 60
)

// Game is one row of GET /api/games/catalog.
type Game struct {
	Game      string   `json:"game"`
	Source    string   `json:"source"`    // campaign | kick | twitch_drops
	Platforms []string `json:"platforms"` // "twitch", "kick" (twitch first)
	Campaigns int      `json:"campaigns"`
	EndsAt    string   `json:"endsAt"` // RFC 3339, "" when unknown
	Live      int      `json:"live"`
}

// Sources says which anonymous sources answered on the last fetch.
type Sources struct {
	Kick        bool `json:"kick"`
	TwitchDrops bool `json:"twitchDrops"`
}

// Result is the GET /api/games/catalog body.
type Result struct {
	FetchedAt string  `json:"fetchedAt"`
	Sources   Sources `json:"sources"`
	Games     []Game  `json:"games"`
}

// Catalog caches the anonymous sources for CacheTTL per process and merges
// them with a profile's own campaigns on every call (that part is local).
type Catalog struct {
	f   Fetcher
	now func() time.Time

	mu          sync.Mutex
	fetched     time.Time
	kick        []Entry
	twitch      []Entry
	sources     Sources
	kickDown    bool // a failure was already logged; log again only after recovery
	twitchDown  bool
	lastRefresh time.Time
}

// New returns a Catalog over f.
func New(f Fetcher) *Catalog { return &Catalog{f: f, now: time.Now} }

// Get returns the merged catalogue. The anonymous sources are fetched when the
// cache is empty or older than CacheTTL, or when refresh is set and the last
// forced refresh is at least RefreshMinGap old; otherwise the cached copy is
// used.
func (c *Catalog) Get(ctx context.Context, own []Entry, refresh bool) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	stale := c.fetched.IsZero() || now.Sub(c.fetched) >= CacheTTL
	if refresh && (c.lastRefresh.IsZero() || now.Sub(c.lastRefresh) >= RefreshMinGap) {
		c.lastRefresh = now
		stale = true
	}
	if stale {
		c.fetch(context.WithoutCancel(ctx), now)
	}
	return Result{
		FetchedAt: c.fetched.UTC().Format(time.RFC3339),
		Sources:   c.sources,
		Games:     Merge(own, c.kick, c.twitch),
	}
}

func (c *Catalog) fetch(ctx context.Context, now time.Time) {
	var wg sync.WaitGroup
	var kick, twitch []Entry
	var kerr, terr error
	wg.Add(2)
	go func() { defer wg.Done(); kick, kerr = c.f.KickCampaigns(ctx) }()
	go func() { defer wg.Done(); twitch, terr = c.f.TwitchDropsStreams(ctx) }()
	wg.Wait()
	c.kick, c.sources.Kick = kick, kerr == nil
	c.twitch, c.sources.TwitchDrops = twitch, terr == nil
	c.kickDown = logSource("kick", kerr, c.kickDown)
	c.twitchDown = logSource("twitch_drops", terr, c.twitchDown)
	c.fetched = now
}

// logSource logs a source failure once per outage and its recovery once.
func logSource(name string, err error, down bool) bool {
	switch {
	case err != nil && !down:
		log.Warn().Err(err).Str("source", name).Msg("games catalog: anonymous source failed")
	case err == nil && down:
		log.Info().Str("source", name).Msg("games catalog: anonymous source recovered")
	}
	return err != nil
}

// Merge folds the three sources into one row per game (Key). Source is
// "campaign" when own knows the game, else "kick" when Kick lists it, else
// "twitch_drops". Campaigns is the profile's own count when it knows the game
// (its discovery already covers Kick), else Kick's anonymous count. Live comes
// from the Twitch stream walk; EndsAt is the latest end seen.
func Merge(own, kick, twitch []Entry) []Game {
	type agg struct {
		g     Game
		plats map[string]bool
		end   time.Time
		own   bool
	}
	byKey := map[string]*agg{}
	var order []string
	add := func(e Entry, src string) {
		key := Key(e.Game)
		if key == "" {
			return
		}
		a := byKey[key]
		if a == nil {
			a = &agg{g: Game{Game: e.Game, Source: src}, plats: map[string]bool{}}
			byKey[key] = a
			order = append(order, key)
		}
		for _, p := range e.Platforms {
			a.plats[p] = true
		}
		switch src {
		case SourceCampaign:
			a.own = true
			a.g.Campaigns = e.Campaigns
		case SourceKick:
			if !a.own {
				a.g.Campaigns += e.Campaigns
			}
		}
		a.g.Live += e.Live
		if e.EndsAt.After(a.end) {
			a.end = e.EndsAt
		}
	}
	for _, e := range own {
		add(e, SourceCampaign)
	}
	for _, e := range kick {
		add(e, SourceKick)
	}
	for _, e := range twitch {
		add(e, SourceTwitchDrops)
	}
	out := make([]Game, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		a.g.Platforms = []string{}
		for _, p := range []string{"twitch", "kick"} {
			if a.plats[p] {
				a.g.Platforms = append(a.g.Platforms, p)
			}
		}
		if !a.end.IsZero() {
			a.g.EndsAt = a.end.UTC().Format(time.RFC3339)
		}
		out = append(out, a.g)
	}
	sort.SliceStable(out, func(i, j int) bool { return Key(out[i].Game) < Key(out[j].Game) })
	return out
}

// Search errors the HTTP layer maps to 400 and 429.
var (
	ErrInvalidQuery = errors.New("invalid query")
	ErrRateLimited  = errors.New("too many searches, slow down")
)

// Searcher runs category searches with a per-query cache (CacheTTL) and a
// process-wide limit of 2 upstream searches per second. Cache hits are never
// limited.
type Searcher struct {
	f   Fetcher
	now func() time.Time

	mu     sync.Mutex
	cache  map[string]searchHit
	recent []time.Time // upstream calls in the last second
}

type searchHit struct {
	at  time.Time
	res []Category
}

// NewSearcher returns a Searcher over f.
func NewSearcher(f Fetcher) *Searcher {
	return &Searcher{f: f, now: time.Now, cache: map[string]searchHit{}}
}

// ValidateQuery trims q and checks its length (2..60 characters).
func ValidateQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	n := utf8.RuneCountInString(q)
	if n < minQuery {
		return "", fmt.Errorf("%w: type at least %d characters", ErrInvalidQuery, minQuery)
	}
	if n > maxQuery {
		return "", fmt.Errorf("%w: at most %d characters", ErrInvalidQuery, maxQuery)
	}
	return q, nil
}

// Search returns the categories matching q.
func (s *Searcher) Search(ctx context.Context, q string) ([]Category, error) {
	q, err := ValidateQuery(q)
	if err != nil {
		return nil, err
	}
	key := Key(q)
	s.mu.Lock()
	now := s.now()
	if hit, ok := s.cache[key]; ok && now.Sub(hit.at) < CacheTTL {
		s.mu.Unlock()
		return hit.res, nil
	}
	keep := s.recent[:0]
	for _, t := range s.recent {
		if now.Sub(t) < time.Second {
			keep = append(keep, t)
		}
	}
	s.recent = keep
	if len(s.recent) >= searchPerSecond {
		s.mu.Unlock()
		return nil, ErrRateLimited
	}
	s.recent = append(s.recent, now)
	s.mu.Unlock()

	res, err := s.f.SearchCategories(ctx, q)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= searchCacheMax {
		for k, v := range s.cache {
			if now.Sub(v.at) >= CacheTTL {
				delete(s.cache, k)
			}
		}
		if len(s.cache) >= searchCacheMax {
			s.cache = map[string]searchHit{}
		}
	}
	s.cache[key] = searchHit{at: now, res: res}
	s.mu.Unlock()
	return res, nil
}
