# Twitch verify-live notes

A living log of the Twitch-specific assumptions that could not be checked from a
headless box. Confirm each on a real, logged-in machine and update this file (and
the referenced source) when reality differs. Set `logging.capture_gql_bodies:
true` to dump raw GraphQL for comparison.

## GraphQL operations (`internal/twitch/gql`)

Operation names matched (by `operationName` only; no persisted-query hashes are
ever sent, so hash drift is a non-issue): `Campaigns`, `CampaignDetails`,
`Inventory`, `CurrentDrop`, `ClaimDropRewards`.

Field-name assumptions to confirm against captured JSON (see
`internal/twitch/gql/types.go`, every struct is marked `// verify live`):

- Campaigns list path: `data.currentUser.dropCampaigns[]` (not edges/node).
- CampaignDetails path: `data.user.dropCampaign` (note: `user`, not
  `currentUser`).
- Game fields: `game.displayName`, `game.slug`.
- Account link: `dropCampaign.accountLinkURL` and/or `self.isAccountConnected`.
- Drops: `timeBasedDrops[].requiredMinutesWatched`,
  `benefitEdges[].benefit.{id,name,imageAssetURL}`, `preconditionDrops[].id`.
- ACL: `allow.channels[].name` - assumed to be the login, lowercased. It may be
  the display name; if channel opens 404, this is why.
- Inventory: `dropCampaignsInProgress[].timeBasedDrops[].self.
  {currentMinutesWatched,isClaimed,dropInstanceID,hasPreconditionsMet}`.
- CurrentDrop: `data.currentUser.dropCurrentSession.
  {dropID,currentMinutesWatched,requiredMinutesWatched}`.

## DOM selectors (`internal/twitch/selectors.go`)

All selectors are best-effort guesses and marked `// verify live`. Confirm in
DevTools on a live channel / the inventory page and fix as needed:

- Player: `video`, play button `button[data-a-target='player-play-pause-button']`.
- Offline gate, player error overlays.
- Raid banner + its Cancel control.
- Drops indicator: chat callout / drops icon in the chat input bar.
- Claim button (channel toast + inventory card).
- Directory channel cards (for any-channel campaigns) and the `?filter=drops`
  query param.

## Player localStorage keys (`internal/twitch/player.go`)

Assumed key names: `volume` (string 0..1), `video-muted`
(`{"default":false}`), `video-quality` (`{"default":"160p30"}`). Confirm these
are the keys the current Twitch player reads. The live `<video>` fallback
(`video.volume = 0.01; video.muted = false`) runs on a timer regardless.

## Capture shim (`jsCaptureShim`)

Hooks `fetch` and `XMLHttpRequest` to buffer responses whose URL contains
`gql.twitch.tv/gql`. Confirm the GraphQL host/path and that batched requests are
a JSON array of `{operationName,...}` objects.

## Behavior

- Only one channel credits progress per account at a time. If you are also
  watching Twitch elsewhere, this app's progress will stall.
- Post-raid progress can stall ~30 min until a refresh; the watcher forces a
  hard refresh after every raid transition to avoid this.
