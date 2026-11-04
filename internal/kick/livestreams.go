package kick

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// livestreamsAPITmpl is the live-streams listing the category page itself
// loads: one page of streams in a category, most viewers first. Undocumented
// like campaignsAPI; fetched from page context so cookies and Cloudflare
// clearance ride along. The %s is the category slug, query-escaped.
const livestreamsAPITmpl = "https://kick.com/stream/livestreams/en?page=1&limit=24&subcategory=%s&sort=desc"

// jsFetchLivestreams returns the page-context fetch for one category's live
// streams, in the same {status, body} wrapper as jsFetchCampaigns.
func jsFetchLivestreams(slug string) string {
	u := fmt.Sprintf(livestreamsAPITmpl, url.QueryEscape(slug))
	return `(async () => {
  try {
    const r = await fetch('` + u + `', { credentials: 'include', headers: { 'Accept': 'application/json' }, signal: AbortSignal.timeout(20000) });
    return JSON.stringify({ status: r.status, body: await r.text() });
  } catch (e) { return JSON.stringify({ status: 0, body: String(e) }); }
})()`
}

// ParseLivestreamsResponse unwraps the {status, body} fetch wrapper and
// returns the live channel logins; a non-200 status is an error so the
// caller can fall back.
func ParseLivestreamsResponse(raw string) ([]string, error) {
	var wrapped struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return nil, fmt.Errorf("kick: decode livestreams wrapper (%.120s): %w", raw, err)
	}
	if wrapped.Status != 200 {
		return nil, fmt.Errorf("kick: livestreams endpoint returned %d (%.120s)", wrapped.Status, wrapped.Body)
	}
	return ParseLivestreams([]byte(wrapped.Body))
}

// ParseLivestreams returns the channel logins of the live streams in a
// livestreams response, in the order served (most viewers first), without
// duplicates. Streams flagged not live are skipped.
func ParseLivestreams(body []byte) ([]string, error) {
	var resp struct {
		Data []struct {
			IsLive  *bool `json:"is_live"`
			Channel struct {
				Slug string `json:"slug"`
				User struct {
					Username string `json:"username"`
				} `json:"user"`
			} `json:"channel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("kick: parse livestreams: %w", err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(resp.Data))
	for _, s := range resp.Data {
		if s.IsLive != nil && !*s.IsLive {
			continue
		}
		login := strings.ToLower(strings.TrimSpace(firstNonEmpty(s.Channel.Slug, s.Channel.User.Username)))
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out, nil
}
