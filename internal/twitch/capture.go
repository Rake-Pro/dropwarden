package twitch

import (
	"bytes"
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

	// Diagnostics: how many GQL responses the shim buffered, and the distinct
	// operation names seen (recognized or not). Discovery logs these so a
	// live capture reveals the real Twitch operation names without guessing.
	RawCount int
	OpsSeen  []string

	// DebugSample holds a truncated campaigns-op response body when it parsed to
	// zero campaigns, so a shape mismatch is diagnosable from one run's log.
	DebugSample string
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
	res.RawCount = len(entries)
	seen := map[string]bool{}
	for _, e := range entries {
		// Twitch batches GraphQL: one HTTP request carries an array of operations
		// and the response is an array of result objects positionally aligned
		// with the request's operationName list (e.Ops). Split the batch so each
		// operation's parser gets ITS OWN result object, not the whole array
		// (which would fail to unmarshal). A non-batched response is a single
		// object paired with e.Ops[0].
		bodies, ops := splitBatch([]byte(e.Body), e.Ops)
		for i, sub := range bodies {
			op := ops[i]
			if op != "" && !seen[op] {
				seen[op] = true
				res.OpsSeen = append(res.OpsSeen, op)
			}
			switch {
			case gql.IsCampaignsOp(op):
				cs, err := gql.ParseCampaigns(sub)
				if err == nil {
					res.Campaigns = append(res.Campaigns, cs...)
				}
				if len(cs) == 0 && res.DebugSample == "" {
					res.DebugSample = truncate(sub, 700)
				}
			case gql.IsDetailsOp(op):
				if c, err := gql.ParseCampaignDetails(sub); err == nil && c.ID != "" {
					res.Details = append(res.Details, c)
				}
			case gql.IsInventoryOp(op):
				if ps, err := gql.ParseInventory(sub); err == nil {
					res.Progress = append(res.Progress, ps...)
				}
			case gql.IsCurrentDropOp(op):
				if p, err := gql.ParseCurrentDrop(sub); err == nil && p != nil {
					res.Progress = append(res.Progress, *p)
				}
			}
		}
	}
	return res, nil
}

// splitBatch aligns response bodies with operation names. If body is a JSON
// array (batched), each element pairs with ops[i] positionally; otherwise the
// whole body pairs with ops[0]. The returned slices are always equal length.
func splitBatch(body []byte, ops []string) (bodies [][]byte, outOps []string) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			for i := range arr {
				bodies = append(bodies, arr[i])
				if i < len(ops) {
					outOps = append(outOps, ops[i])
				} else {
					outOps = append(outOps, "")
				}
			}
			return bodies, outOps
		}
	}
	op := ""
	if len(ops) > 0 {
		op = ops[0]
	}
	return [][]byte{body}, []string{op}
}

// truncate returns up to n bytes of b as a string, for diagnostic logging.
func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
