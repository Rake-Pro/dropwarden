package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseKickSample(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	got, err := ParseKick(readSample(t, "kick_campaigns.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	// Sample: null-category promo (skipped), expired Rust (skipped), active
	// VALORANT, Minecraft, World of Warcraft: Forever.
	want := map[string]string{
		"VALORANT":                   "2026-10-18T11:00:00Z",
		"Minecraft":                  "2026-10-23T00:00:00Z",
		"World of Warcraft: Forever": "2026-10-31T00:00:00Z",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %d", len(got), got, len(want))
	}
	for _, e := range got {
		end, ok := want[e.Game]
		if !ok || e.EndsAt.Format(time.RFC3339) != end || e.Campaigns != 1 || len(e.Platforms) != 1 || e.Platforms[0] != "kick" {
			t.Errorf("unexpected entry %+v", e)
		}
	}
}

func TestParseKickTolerant(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	data := `{"data":[
		{"name":"a","ends_at":"2026-10-01T00:00:00Z","status":"active","game":{"name":"Rust"}},
		{"name":"b","ends_at":"2026-10-05T00:00:00Z","status":"active","category":{"name":" rust "}},
		{"name":"c","ends_at":"2026-09-01T00:00:00Z","status":"active","category":{"name":"Old"}},
		{"name":"d","ends_at":"","status":"upcoming","category":{"name":"NoEnd"}}
	]}`
	got, err := ParseKick([]byte(data), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Game != "Rust" || got[0].Campaigns != 2 || got[0].EndsAt.Day() != 5 || got[1].Game != "NoEnd" || !got[1].EndsAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
	if _, err := ParseKick([]byte(`{"message":"nope"}`), now); err == nil {
		t.Fatal("missing data array should fail")
	}
}

func TestParseStreamsSample(t *testing.T) {
	p, err := ParseStreams(readSample(t, "twitch_streams.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Games) != 8 || p.Games[0] != "Grand Theft Auto V" || !p.HasNext || p.Cursor == "" {
		t.Fatalf("got %+v", p)
	}
	if _, err := ParseStreams(readSample(t, "twitch_integrity.json")); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("integrity error not surfaced: %v", err)
	}
	p, err = ParseStreams([]byte(`{"data":{"streams":{"edges":[{"cursor":"x","node":{"game":null}}],"pageInfo":{"hasNextPage":false}}}}`))
	if err != nil || len(p.Games) != 0 || p.HasNext {
		t.Fatalf("null game: %+v %v", p, err)
	}
}

func TestParseSearchSample(t *testing.T) {
	got, err := ParseSearch(readSample(t, "twitch_search.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Game != "Rust" || got[0].ID != "263490" {
		t.Fatalf("got %+v", got)
	}
	if got[0].BoxArt != "https://static-cdn.jtvnw.net/ttv-boxart/263490_IGDB-52x72.jpg" {
		t.Fatalf("boxArt %q", got[0].BoxArt)
	}
}

// gqlServer answers every streams page with one stream and hasNextPage true,
// counting requests and checking the anonymous headers.
func gqlServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Client-ID") != twitchWebClientID || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("bad headers %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("body %s: %v", body, err)
		}
		if strings.Contains(req.Query, "searchCategories") {
			q, _ := req.Variables["q"].(string)
			_, _ = io.WriteString(w, `{"data":{"searchCategories":{"edges":[{"node":{"id":"1","displayName":"`+strings.ToUpper(q[:1])+`","boxArtURL":"x-{width}x{height}.jpg"}}]}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"streams":{"edges":[{"cursor":"c","node":{"game":{"id":"1","displayName":"Rust"},"viewersCount":5}}],"pageInfo":{"hasNextPage":true}}}}`)
	}))
}

func TestTwitchPaginationStopsAtFivePages(t *testing.T) {
	var calls atomic.Int32
	srv := gqlServer(t, &calls)
	defer srv.Close()
	c := NewClient()
	c.GQLURL = srv.URL
	got, err := c.TwitchDropsStreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != MaxStreamPages {
		t.Fatalf("pages fetched = %d, want %d", calls.Load(), MaxStreamPages)
	}
	if len(got) != 1 || got[0].Live != MaxStreamPages || got[0].Platforms[0] != "twitch" {
		t.Fatalf("got %+v", got)
	}
}

func TestTwitchLaterPageFailureKeepsFirst(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = w.Write(readSample(t, "twitch_streams.json"))
			return
		}
		_, _ = w.Write(readSample(t, "twitch_integrity.json"))
	}))
	defer srv.Close()
	c := NewClient()
	c.GQLURL = srv.URL
	got, err := c.TwitchDropsStreams(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("err %v after %d calls", err, n)
	}
	live := 0
	for _, e := range got {
		live += e.Live
	}
	if live != 8 {
		t.Fatalf("live %d, entries %+v", live, got)
	}
}

func TestKickClientHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Mozilla/5.0") {
			t.Errorf("bad headers %v", r.Header)
		}
		_, _ = w.Write(readSample(t, "kick_campaigns.json"))
	}))
	defer srv.Close()
	c := NewClient()
	c.KickURL = srv.URL
	c.Now = func() time.Time { return time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC) }
	got, err := c.KickCampaigns(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestMerge(t *testing.T) {
	end1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end2 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	own := []Entry{{Game: "Rust", Platforms: []string{"twitch"}, Campaigns: 3, EndsAt: end1}}
	kick := []Entry{
		{Game: " rust", Platforms: []string{"kick"}, Campaigns: 2, EndsAt: end2},
		{Game: "VALORANT", Platforms: []string{"kick"}, Campaigns: 1, EndsAt: end1},
	}
	twitch := []Entry{
		{Game: "RUST", Platforms: []string{"twitch"}, Live: 4},
		{Game: "Valorant", Platforms: []string{"twitch"}, Live: 2},
		{Game: "Dota 2", Platforms: []string{"twitch"}, Live: 7},
	}
	got := Merge(own, kick, twitch)
	want := []Game{
		{Game: "Dota 2", Source: SourceTwitchDrops, Platforms: []string{"twitch"}, Live: 7},
		{Game: "Rust", Source: SourceCampaign, Platforms: []string{"twitch", "kick"}, Campaigns: 3, EndsAt: "2026-10-09T00:00:00Z", Live: 4},
		{Game: "VALORANT", Source: SourceKick, Platforms: []string{"twitch", "kick"}, Campaigns: 1, EndsAt: "2026-10-01T00:00:00Z", Live: 2},
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("merge\n got %s\nwant %s", gj, wj)
	}
}

type fakeFetcher struct {
	kick, twitch, search atomic.Int32
	kickErr              error
}

func (f *fakeFetcher) KickCampaigns(context.Context) ([]Entry, error) {
	f.kick.Add(1)
	if f.kickErr != nil {
		return nil, f.kickErr
	}
	return []Entry{{Game: "VALORANT", Platforms: []string{"kick"}, Campaigns: 1}}, nil
}

func (f *fakeFetcher) TwitchDropsStreams(context.Context) ([]Entry, error) {
	f.twitch.Add(1)
	return []Entry{{Game: "Dota 2", Platforms: []string{"twitch"}, Live: 1}}, nil
}

func (f *fakeFetcher) SearchCategories(_ context.Context, q string) ([]Category, error) {
	f.search.Add(1)
	return []Category{{Game: q}}, nil
}

func TestCatalogCacheAndRefreshThrottle(t *testing.T) {
	f := &fakeFetcher{kickErr: errors.New("boom")}
	c := New(f)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	r := c.Get(ctx, nil, false)
	if f.kick.Load() != 1 || r.Sources.Kick || !r.Sources.TwitchDrops || len(r.Games) != 1 {
		t.Fatalf("first get: %+v", r)
	}
	if r.FetchedAt != "2026-09-26T12:00:00Z" {
		t.Fatalf("fetchedAt %q", r.FetchedAt)
	}
	f.kickErr = nil
	now = now.Add(5 * time.Minute)
	c.Get(ctx, nil, false)
	if f.kick.Load() != 1 {
		t.Fatal("cache should serve within 10 min")
	}
	r = c.Get(ctx, nil, true) // first forced refresh goes through
	if f.kick.Load() != 2 || !r.Sources.Kick || len(r.Games) != 2 {
		t.Fatalf("refresh: calls %d %+v", f.kick.Load(), r)
	}
	now = now.Add(30 * time.Second)
	c.Get(ctx, nil, true) // within 60 s: cached
	if f.kick.Load() != 2 {
		t.Fatal("refresh inside 60 s must use the cache")
	}
	now = now.Add(31 * time.Second)
	c.Get(ctx, nil, true)
	if f.kick.Load() != 3 {
		t.Fatal("refresh after 60 s should fetch")
	}
	now = now.Add(CacheTTL)
	own := []Entry{{Game: "valorant", Platforms: []string{"twitch"}, Campaigns: 2}}
	r = c.Get(ctx, own, false)
	if f.kick.Load() != 4 || f.twitch.Load() != 4 {
		t.Fatal("expired cache should fetch")
	}
	if r.Games[1].Source != SourceCampaign || r.Games[1].Campaigns != 2 {
		t.Fatalf("own merge: %+v", r.Games)
	}
}

func TestSearchValidationCacheAndLimit(t *testing.T) {
	f := &fakeFetcher{}
	s := NewSearcher(f)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	for _, q := range []string{"", " a ", strings.Repeat("x", 61)} {
		if _, err := s.Search(ctx, q); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("q %q: err %v", q, err)
		}
	}
	if _, err := s.Search(ctx, strings.Repeat("x", 60)); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Search(ctx, "  Rust "); err != nil || res[0].Game != "Rust" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := s.Search(ctx, "halo"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third upstream search in 1 s: %v", err)
	}
	if _, err := s.Search(ctx, "rust"); err != nil {
		t.Fatalf("cache hit must not be limited: %v", err)
	}
	if f.search.Load() != 2 {
		t.Fatalf("upstream calls %d", f.search.Load())
	}
	now = now.Add(time.Second)
	if _, err := s.Search(ctx, "halo"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(CacheTTL)
	s.Search(ctx, "rust")
	if f.search.Load() != 4 {
		t.Fatalf("expired entry should refetch, calls %d", f.search.Load())
	}
}

func TestSearchClientEscapesQuery(t *testing.T) {
	var calls atomic.Int32
	srv := gqlServer(t, &calls)
	defer srv.Close()
	c := NewClient()
	c.GQLURL = srv.URL
	got, err := c.SearchCategories(context.Background(), `a"b\c`)
	if err != nil || len(got) != 1 || got[0].BoxArt != "x-52x72.jpg" {
		t.Fatalf("%+v %v", got, err)
	}
}
