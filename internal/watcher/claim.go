package watcher

import (
	"context"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// runClaiming claims the completed drop. It prefers an on-screen Claim button
// (most human), falling back to the inventory page. The GQL mutation replay is
// intentionally not used unless explicitly enabled (documented bot-block risk),
// and even then is left as a future step for MVP.
func (e *Engine) runClaiming(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateCooldown, "nothing to claim"
	}
	drop := e.cur.drop

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
		// Do not lose progress: the reward remains claimable server-side. Mark
		// the item for the user and move on rather than hammering the claim flow.
		_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemUnavailable, "claim button not found; will retry on next inventory pass")
		e.deps.Log.Warn().Str("drop", drop.ID).Msg("claim: could not confirm; leaving for retry")
		return StateCooldown, "claim deferred"
	}

	now := e.deps.Now()
	claim := domain.Claim{
		DropID:     drop.ID,
		CampaignID: e.cur.campaign.ID,
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
