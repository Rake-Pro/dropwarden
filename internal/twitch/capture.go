package twitch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch/gql"
)

// capturedEntry mirrors one record the in-page shim buffered.
type capturedEntry struct {
	Ops  []string `json:"ops"`
	Body string   `json:"body"`
	At   int64    `json:"at"`
}

// CaptureResult holds everything drained from the page's own GQL traffic during
// a navigation. Slices may be empty; the caller reconciles what it got.
type CaptureResult struct {
	Campaigns []domain.Campaign // from Campaigns list op
	Details   []domain.Campaign // from CampaignDetails op (full drops + ACL)
	Progress  []domain.Progress // from Inventory / CurrentDrop ops
}

// Drain reads and clears the in-page GQL capture buffer, parsing every
// recognized operation. Unrecognized ops and unparseable bodies are skipped, so
// a single bad frame never fails the drain.
func Drain(ctx context.Context, sess interface {
	Eval(context.Context, string, any) error
}) (CaptureResult, error) {
	var raw string
	if err := sess.Eval(ctx, jsDrainCapture, &raw); err != nil {
		return CaptureResult{}, fmt.Errorf("twitch: drain capture: %w", err)
	}
	var entries []capturedEntry
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return CaptureResult{}, fmt.Errorf("twitch: decode capture buffer: %w", err)
		}
	}

	var res CaptureResult
	for _, e := range entries {
		body := []byte(e.Body)
		// A batched response body is an array positionally aligned with e.Ops;
		// a single response is one object. The gql parsers accept the whole body
		// and pull the relevant node, so we route by whether ANY op matches.
		for _, op := range e.Ops {
			switch op {
			case gql.OpCampaigns:
				if cs, err := gql.ParseCampaigns(body); err == nil {
					res.Campaigns = append(res.Campaigns, cs...)
				}
			case gql.OpCampaignDetails:
				if c, err := gql.ParseCampaignDetails(body); err == nil && c.ID != "" {
					res.Details = append(res.Details, c)
				}
			case gql.OpInventory:
				if ps, err := gql.ParseInventory(body); err == nil {
					res.Progress = append(res.Progress, ps...)
				}
			case gql.OpCurrentDrop:
				if p, err := gql.ParseCurrentDrop(body); err == nil && p != nil {
					res.Progress = append(res.Progress, *p)
				}
			}
		}
	}
	return res, nil
}
