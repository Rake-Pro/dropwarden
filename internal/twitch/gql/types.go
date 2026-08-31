package gql

// The structs below mirror the documented shape of Twitch's GraphQL
// responses (as reverse-engineered by community tools such as
// TwitchDropsMiner). No live capture was available in this environment, so
// every struct is marked "verify live" with the fields most likely to need
// correction once checked against a real response body. All fields use
// pointers or plain zero-value types so a missing/renamed field degrades
// rather than breaking JSON decoding.

// campaignGame is the game object embedded in campaign responses.
//
// verify live: confirm "displayName" vs "name", and that "slug" exists on
// both the list and details game objects.
type campaignGame struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Slug        string `json:"slug"`
}

// campaignSelf carries viewer-specific campaign state in the list response.
//
// verify live: confirm this field is actually named "self" and that
// isAccountConnected is the correct key (vs. accountLinkURL presence).
type campaignSelf struct {
	IsAccountConnected bool `json:"isAccountConnected"`
}

// campaignsResponse is the shape of the "Campaigns" operation.
//
// verify live: confirm the path is data.currentUser.dropCampaigns and not
// nested under an edges/node connection.
type campaignsResponse struct {
	Data struct {
		CurrentUser *struct {
			DropCampaigns []campaignListEntry `json:"dropCampaigns"`
		} `json:"currentUser"`
	} `json:"data"`
}

// campaignListEntry is one entry in the Campaigns list response.
type campaignListEntry struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Status  string        `json:"status"`
	StartAt string        `json:"startAt"`
	EndAt   string        `json:"endAt"`
	Game    *campaignGame `json:"game"`
	Self    *campaignSelf `json:"self"`
}

// channelRef is a minimal channel reference as it appears in a campaign ACL.
//
// verify live: confirm whether "name" is the login (lowercase URL slug) or
// the display name. ParseCampaignDetails currently assumes it must be
// lowercased to form Login.
type channelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// benefitEdge wraps a single benefit in a timeBasedDrop's benefitEdges list.
//
// verify live: confirm "imageAssetURL" is the correct key (Twitch has used
// both imageAssetURL and imageAssetUrl casings historically).
type benefitEdge struct {
	Benefit *struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ImageAssetURL string `json:"imageAssetURL"`
	} `json:"benefit"`
}

// preconditionDrop is a drop-id reference gating another drop.
type preconditionDrop struct {
	ID string `json:"id"`
}

// timeBasedDrop is one drop tier within a campaign's details response.
//
// verify live: confirm "requiredMinutesWatched" is the exact key (vs
// requiredWatchMinutes / requiredSubs for non-time-based drops, which this
// package does not model).
type timeBasedDrop struct {
	ID                     string             `json:"id"`
	Name                   string             `json:"name"`
	RequiredMinutesWatched int                `json:"requiredMinutesWatched"`
	BenefitEdges           []benefitEdge      `json:"benefitEdges"`
	PreconditionDrops      []preconditionDrop `json:"preconditionDrops"`
	StartAt                string             `json:"startAt"`
	EndAt                  string             `json:"endAt"`
	Self                   *inventoryDropSelf `json:"self"`
}

// campaignDetailsResponse is the shape of the "CampaignDetails" operation.
//
// verify live: confirm the path is data.user.dropCampaign (not
// data.currentUser.dropCampaign) for this particular query.
type campaignDetailsResponse struct {
	Data struct {
		User *struct {
			DropCampaign *dropCampaignDetails `json:"dropCampaign"`
		} `json:"user"`
	} `json:"data"`
}

type dropCampaignDetails struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Status         string        `json:"status"`
	StartAt        string        `json:"startAt"`
	EndAt          string        `json:"endAt"`
	AccountLinkURL string        `json:"accountLinkURL"`
	Game           *campaignGame `json:"game"`
	Self           *campaignSelf `json:"self"`
	Allow          *struct {
		Channels []channelRef `json:"channels"`
	} `json:"allow"`
	TimeBasedDrops []timeBasedDrop `json:"timeBasedDrops"`
}

// inventoryDropSelf carries the viewer's progress on one in-progress drop.
//
// verify live: confirm "hasPreconditionsMet" and "dropInstanceID" keys;
// some captures show "dropInstanceID" only present once minutes > 0.
type inventoryDropSelf struct {
	CurrentMinutesWatched int     `json:"currentMinutesWatched"`
	IsClaimed             bool    `json:"isClaimed"`
	DropInstanceID        *string `json:"dropInstanceID"`
	HasPreconditionsMet   *bool   `json:"hasPreconditionsMet"`
}

// inventoryTimeBasedDrop is one drop within an in-progress campaign in the
// Inventory response.
type inventoryTimeBasedDrop struct {
	ID                     string             `json:"id"`
	RequiredMinutesWatched int                `json:"requiredMinutesWatched"`
	Self                   *inventoryDropSelf `json:"self"`
}

// inventoryCampaign is one in-progress campaign in the Inventory response.
type inventoryCampaign struct {
	ID             string                   `json:"id"`
	TimeBasedDrops []inventoryTimeBasedDrop `json:"timeBasedDrops"`
}

// inventoryResponse is the shape of the "Inventory" operation.
//
// verify live: confirm "gameEventDrops" shape; this package does not parse
// it (only time-based drops are in scope for dropwarden).
type inventoryResponse struct {
	Data struct {
		CurrentUser *struct {
			Inventory *struct {
				DropCampaignsInProgress []inventoryCampaign `json:"dropCampaignsInProgress"`
				GameEventDrops          []struct {
					ID string `json:"id"`
				} `json:"gameEventDrops"`
			} `json:"inventory"`
		} `json:"currentUser"`
	} `json:"data"`
}

// currentDropChannel identifies the channel a current-drop session is tied
// to.
type currentDropChannel struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

// dropCurrentSession is the viewer's live drop-watching session.
//
// verify live: confirm the field is "dropID" (not "id") for this query, and
// that currentMinutesWatched/requiredMinutesWatched sit directly on this
// object rather than nested under a "drop" sub-object.
type dropCurrentSession struct {
	DropID                 string              `json:"dropID"`
	CurrentMinutesWatched  int                 `json:"currentMinutesWatched"`
	RequiredMinutesWatched int                 `json:"requiredMinutesWatched"`
	Channel                *currentDropChannel `json:"channel"`
}

// currentDropResponse is the shape of the "CurrentDrop" operation.
//
// verify live: confirm the path is data.currentUser.dropCurrentSession.
type currentDropResponse struct {
	Data struct {
		CurrentUser *struct {
			DropCurrentSession *dropCurrentSession `json:"dropCurrentSession"`
		} `json:"currentUser"`
	} `json:"data"`
}

// gqlRequest is a single element of a (possibly batched) GQL POST body.
type gqlRequest struct {
	OperationName string `json:"operationName"`
}
