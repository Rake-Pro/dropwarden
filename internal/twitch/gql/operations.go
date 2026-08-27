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

// AllOps lists every operation name this package recognizes.
var AllOps = []string{
	OpCampaigns,
	OpCampaignDetails,
	OpInventory,
	OpCurrentDrop,
	OpClaimDropRewards,
}

// Recognized reports whether op is one of the operation names this package
// knows how to handle.
func Recognized(op string) bool {
	for _, o := range AllOps {
		if o == op {
			return true
		}
	}
	return false
}
