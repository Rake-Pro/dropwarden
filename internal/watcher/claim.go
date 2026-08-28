package watcher

import (
	"context"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// claimTimeout bounds the ENTIRE claim attempt (panel toggle, in-page click,
// inventory fallback). A live run spent 9+ minutes stuck in a single claim
// click; nothing in this state is worth more than a minute of stream time.
const claimTimeout = 60 * time.Second

// URLInventoryClaim is where the user can claim manually when we could not.
const URLInventoryClaim = "https://www.twitch.tv/drops/inventory"

// runClaiming makes ONE bounded attempt to claim the completed drop: on-screen
// Claim button first (most human), then the inventory page. On failure the item
// is marked needs_action for the user to reconcile manually and the engine
// moves on - no automatic retries (user decision 2026-08-27). The GQL mutation
// replay is intentionally not used unless explicitly enabled (documented
// bot-block risk), and even then is left as a future step for MVP.
func (e *Engine) runClaiming(parent context.Context) (State, string) {
	if e.cur == nil {
		return StateCooldown, "nothing to claim"
	}
	drop := e.cur.drop
	ctx, cancel := context.WithTimeout(parent, claimTimeout)
	defer cancel()

	// The in-page claim button lives in the drops panel of a visible window:
	// restore when running minimized and open the panel before looking.
	e.setWindowMinimized(ctx, false)
	_ = e.deps.Ctrl.ToggleDropsPanel(ctx)
	claimed, err := e.deps.Ctrl.ClaimVisible(ctx)
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: visible attempt failed")
	}
	if !claimed {
		// Fall back to the inventory page during this cooldown-like moment.
		claimID := ""
		if p, err := e.deps.Store.GetProgress(drop.ID); err == nil && p != nil {
			claimID = p.ClaimID
		}
		if _, err := e.deps.Pages.ReadInventory(ctx); err != nil {
			e.deps.Log.Warn().Err(err).Msg("claim: open inventory failed")
		}
		claimed, err = e.deps.Ctrl.ClaimInInventory(ctx, claimID)
		if err != nil {
			e.deps.Log.Warn().Err(err).Msg("claim: inventory attempt failed")
		}
	}

	if !claimed {
		// The reward remains claimable server-side; surface it to the user and
		// move on to the next drop instead of retrying.
		reason := "completed - claim it manually in the Twitch inventory"
		_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemNeedsAction, reason)
		e.deps.Emit.Emit(EvNeedsAction, NeedsActionPayload{DropID: drop.ID, Reason: reason, URL: URLInventoryClaim})
		e.deps.Log.Warn().Str("drop", drop.ID).Msg("claim: could not confirm; flagged for manual reconcile")
		return StateCooldown, "claim needs manual reconcile"
	}

	now := e.deps.Now()
	claim := domain.Claim{
		DropID:     drop.ID,
		CampaignID: e.cur.campaign.ID,
		Platform:   e.deps.Platform,
		DropName:   drop.Name,
		GameName:   e.cur.campaign.GameName,
		Benefits:   drop.Benefits,
		ClaimedAt:  now,
	}
	if err := e.deps.Store.AppendClaim(claim); err != nil {
		e.deps.Log.Warn().Err(err).Msg("claim: record failed")
	}
	if p, err := e.deps.Store.GetProgress(drop.ID); err == nil && p != nil {
		p.Claimable = false
		p.ClaimedAt = &now
		_ = e.deps.Store.SaveProgress(*p)
	}
	_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemClaimed, "claimed")

	// Record the claim on the current session too.
	e.endSession(domain.OutcomeClaimed, "claimed "+drop.Name)

	e.deps.Emit.Emit(EvClaimed, ClaimedPayload{DropID: drop.ID, CampaignID: e.cur.campaign.ID, Name: drop.Name})
	e.deps.Log.Info().Str("drop", drop.Name).Str("game", e.cur.campaign.GameName).Msg("drop claimed")

	return StateCooldown, "claimed " + drop.Name
}
