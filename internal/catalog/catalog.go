// Package catalog answers the Games picker's "what else has drops?" questions
// from ANONYMOUS public endpoints: Kick's public drops-campaign list and
// Twitch's public GraphQL (drops-enabled live streams, category search). No
// cookies, no auth, no browser. Every call is on demand from an HTTP route and
// cached; nothing here runs on a timer.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// Public endpoints. Client fields override them in tests.
	DefaultKickURL = "https://web.kick.com/api/v1/drops/campaigns"
	DefaultGQLURL  = "https://gql.twitch.tv/gql"

	// twitchWebClientID is the public client id of twitch.tv's own web app;
	// it is what anonymous (logged-out) twitch.tv visitors send.
	twitchWebClientID = "kimne78kx3ncx6brgo4mv6wki5h1ko"
	userAgent         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

	requestTimeout = 10 * time.Second
	// MaxStreamPages caps the drops-enabled stream walk (30 streams a page).
	MaxStreamPages = 5
	streamPageSize = 30
	searchFirst    = 10
	maxBody        = 4 << 20
)

// Source values of a catalogue row.
const (
	SourceCampaign    = "campaign"     // the profile's own discovery knows the game
	SourceKick        = "kick"         // Kick's anonymous campaign list
	SourceTwitchDrops = "twitch_drops" // Twitch drops-enabled streams live now
)

// Entry is one game as a single source sees it.
type Entry struct {
	Game      string
	Platforms []string // "twitch", "kick"
	Campaigns int
	EndsAt    time.Time // latest campaign end, zero when unknown
	Live      int       // drops-enabled streams live now (Twitch)
}

// Category is one Twitch category search hit.
type Category struct {
	Game   string `json:"game"`
	ID     string `json:"id"`
	BoxArt string `json:"boxArt"`
}

// Fetcher is the network side, an interface so tests swap in httptest servers
// or fakes.
type Fetcher interface {
	KickCampaigns(ctx context.Context) ([]Entry, error)
	TwitchDropsStreams(ctx context.Context) ([]Entry, error)
	SearchCategories(ctx context.Context, q string) ([]Category, error)
}

// Client is the real Fetcher: plain net/http, anonymous, 10 s per request.
type Client struct {
	HTTP    *http.Client
	KickURL string
	GQLURL  string
	Now     func() time.Time
}

// NewClient returns a Client against the public endpoints.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: requestTimeout}, KickURL: DefaultKickURL, GQLURL: DefaultGQLURL, Now: time.Now}
}

func (c *Client) do(ctx context.Context, method, url string, body []byte, gql bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if gql {
		req.Header.Set("Client-ID", twitchWebClientID)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return data, nil
}

func (c *Client) gql(ctx context.Context, query string, vars map[string]any) ([]byte, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, c.GQLURL, body, true)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// KickCampaigns reads Kick's anonymous drops-campaign list, one Entry per game.
func (c *Client) KickCampaigns(ctx context.Context) ([]Entry, error) {
	data, err := c.do(ctx, http.MethodGet, c.KickURL, nil, false)
	if err != nil {
		return nil, err
	}
	return ParseKick(data, c.now())
}

// kickCampaign keeps only what the catalogue needs. The game lives in
// category.name (observed 2026-09-26); game.name is a tolerant fallback.
type kickCampaign struct {
	Name     string `json:"name"`
	EndsAt   string `json:"ends_at"`
	Status   string `json:"status"`
	Category *struct {
		Name string `json:"name"`
	} `json:"category"`
	Game *struct {
		Name string `json:"name"`
	} `json:"game"`
}

// ParseKick aggregates a Kick campaigns response per game, skipping expired
// campaigns and campaigns without a category (Kick's own platform promos).
func ParseKick(data []byte, now time.Time) ([]Entry, error) {
	var resp struct {
		Data []kickCampaign `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kick campaigns: %w", err)
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("kick campaigns: no data array")
	}
	var out []Entry
	idx := map[string]int{}
	for _, kc := range resp.Data {
		name := ""
		if kc.Category != nil {
			name = strings.TrimSpace(kc.Category.Name)
		}
		if name == "" && kc.Game != nil {
			name = strings.TrimSpace(kc.Game.Name)
		}
		if name == "" || strings.EqualFold(kc.Status, "expired") {
			continue
		}
		end, _ := time.Parse(time.RFC3339, kc.EndsAt)
		if !end.IsZero() && end.Before(now) {
			continue
		}
		key := Key(name)
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, Entry{Game: name, Platforms: []string{"kick"}})
		}
		out[i].Campaigns++
		if end.After(out[i].EndsAt) {
			out[i].EndsAt = end
		}
	}
	return out, nil
}

const streamsQuery = `query($after:Cursor){ streams(first:30, after:$after, options:{sort:VIEWER_COUNT, tags:["DropsEnabled"]}){ edges{ cursor node{ game{ id displayName } viewersCount } } pageInfo{ hasNextPage } } }`

// StreamPage is one parsed page of drops-enabled streams.
type StreamPage struct {
	Games   []string // one per stream with a game, in order
	Cursor  string   // last edge's cursor
	HasNext bool
}

// ParseStreams parses one streams page (plain or batched response).
func ParseStreams(data []byte) (StreamPage, error) {
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Streams *struct {
				Edges []struct {
					Cursor string `json:"cursor"`
					Node   struct {
						Game *struct {
							DisplayName string `json:"displayName"`
						} `json:"game"`
					} `json:"node"`
				} `json:"edges"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"streams"`
		} `json:"data"`
	}
	if err := json.Unmarshal(unbatch(data), &resp); err != nil {
		return StreamPage{}, fmt.Errorf("twitch streams: %w", err)
	}
	if resp.Data.Streams == nil {
		if len(resp.Errors) > 0 {
			return StreamPage{}, fmt.Errorf("twitch streams: %s", resp.Errors[0].Message)
		}
		return StreamPage{}, fmt.Errorf("twitch streams: no streams in response")
	}
	var p StreamPage
	for _, e := range resp.Data.Streams.Edges {
		p.Cursor = e.Cursor
		if e.Node.Game == nil || strings.TrimSpace(e.Node.Game.DisplayName) == "" {
			continue
		}
		p.Games = append(p.Games, strings.TrimSpace(e.Node.Game.DisplayName))
	}
	p.HasNext = resp.Data.Streams.PageInfo.HasNextPage && p.Cursor != ""
	return p, nil
}

// TwitchDropsStreams walks up to MaxStreamPages pages of drops-enabled live
// streams and counts streams per game. Anonymous requests past page 1 are
// often refused with "failed integrity check" (observed 2026-09-26); a
// failure after the first page keeps what was read and stops.
func (c *Client) TwitchDropsStreams(ctx context.Context) ([]Entry, error) {
	var out []Entry
	idx := map[string]int{}
	var after any
	for page := 0; page < MaxStreamPages; page++ {
		data, err := c.gql(ctx, streamsQuery, map[string]any{"after": after})
		var p StreamPage
		if err == nil {
			p, err = ParseStreams(data)
		}
		if err != nil {
			if page == 0 {
				return nil, err
			}
			break
		}
		for _, g := range p.Games {
			key := Key(g)
			i, ok := idx[key]
			if !ok {
				i = len(out)
				idx[key] = i
				out = append(out, Entry{Game: g, Platforms: []string{"twitch"}})
			}
			out[i].Live++
		}
		if !p.HasNext {
			break
		}
		after = p.Cursor
	}
	return out, nil
}

const searchQuery = `query($q:String!){ searchCategories(query:$q, first:10){ edges{ node{ id displayName boxArtURL } } } }`

// SearchCategories runs Twitch's anonymous category search.
func (c *Client) SearchCategories(ctx context.Context, q string) ([]Category, error) {
	data, err := c.gql(ctx, searchQuery, map[string]any{"q": q})
	if err != nil {
		return nil, err
	}
	return ParseSearch(data)
}

// ParseSearch parses a searchCategories response; box art is sized 52x72.
func ParseSearch(data []byte) ([]Category, error) {
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			SearchCategories *struct {
				Edges []struct {
					Node struct {
						ID          string `json:"id"`
						DisplayName string `json:"displayName"`
						BoxArtURL   string `json:"boxArtURL"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"searchCategories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(unbatch(data), &resp); err != nil {
		return nil, fmt.Errorf("twitch search: %w", err)
	}
	if resp.Data.SearchCategories == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("twitch search: %s", resp.Errors[0].Message)
		}
		return nil, fmt.Errorf("twitch search: no results object")
	}
	out := []Category{}
	for _, e := range resp.Data.SearchCategories.Edges {
		if e.Node.DisplayName == "" {
			continue
		}
		out = append(out, Category{
			Game:   e.Node.DisplayName,
			ID:     e.Node.ID,
			BoxArt: strings.ReplaceAll(e.Node.BoxArtURL, "{width}x{height}", "52x72"),
		})
	}
	return out, nil
}

// unbatch returns the first element of a batched GQL response ([{...}]).
func unbatch(data []byte) []byte {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || t[0] != '[' {
		return data
	}
	var arr []json.RawMessage
	if json.Unmarshal(t, &arr) != nil || len(arr) == 0 {
		return data
	}
	return arr[0]
}

// Key is the merge key: trimmed, case-insensitive game name.
func Key(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
