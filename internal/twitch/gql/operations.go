// Package gql parses Twitch GraphQL responses into dropwarden's domain
// types. It does no networking and pulls in no third-party dependencies:
// stdlib encoding/json plus internal/domain only.
package gql

// Recognized persisted-query operation names. Twitch's web client sends
// these as the "operationName" field alongside a persisted-query hash. We
// match on operationName only and never send or verify the hash, so this
// package is immune to Twitch rotating the persisted-query hashes (a common
// break for scrapers that pin them).
const (
	OpCampaigns        = "Campaigns"
	OpCampaignDetails  = "CampaignDetails"
	OpInventory        = "Inventory"
	OpCurrentDrop      = "CurrentDrop"
	OpClaimDropRewards = "ClaimDropRewards"
)

// AllOps lists the canonical operation names this package recognizes.
var AllOps = []string{
	OpCampaigns,
	OpCampaignDetails,
	OpInventory,
	OpCurrentDrop,
	OpClaimDropRewards,
}

// Twitch's real web client uses longer operation names than the short canonical
// ones above; we accept both so capture keeps working whether Twitch sends the
// dashboard as "ViewerDropsDashboard" or a future/legacy "Campaigns". Matching is
// on operationName only - hashes are never sent - so this stays drift-proof.
// verify live: confirm the exact names in a real capture (see the debug log that
// discovery prints of every op it observed).
var (
	campaignsOps   = set(OpCampaigns, "ViewerDropsDashboard")
	detailsOps     = set(OpCampaignDetails, "DropCampaignDetails", "DropCampaignDetails_Override")
	inventoryOps   = set(OpInventory, "Inventory")
	currentDropOps = set(OpCurrentDrop, "DropCurrentSessionContext")
	claimOps       = set(OpClaimDropRewards, "DropsPage_ClaimDropRewards")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// IsCampaignsOp reports whether op is the drops-dashboard campaigns list.
func IsCampaignsOp(op string) bool { return campaignsOps[op] }

// IsDetailsOp reports whether op is a single-campaign details query.
func IsDetailsOp(op string) bool { return detailsOps[op] }

// IsInventoryOp reports whether op is the inventory query.
func IsInventoryOp(op string) bool { return inventoryOps[op] }

// IsCurrentDropOp reports whether op is the current-watch-session query.
func IsCurrentDropOp(op string) bool { return currentDropOps[op] }

// IsClaimOp reports whether op is the claim mutation.
func IsClaimOp(op string) bool { return claimOps[op] }

// Recognized reports whether op is any operation name this package handles
// (canonical or real-client alias).
func Recognized(op string) bool {
	return IsCampaignsOp(op) || IsDetailsOp(op) || IsInventoryOp(op) || IsCurrentDropOp(op) || IsClaimOp(op)
}
