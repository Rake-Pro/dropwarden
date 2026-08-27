package store

// schemaVersion is bumped whenever migrations change. Migrations are applied
// in order on Open; each is idempotent via IF NOT EXISTS so re-running is safe.
const schemaVersion = 1

// migrations are applied in slice order. Index+1 is the version each brings the
// database to. Keep every statement idempotent.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS campaigns (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		game_id         TEXT,
		game_name       TEXT,
		game_slug       TEXT,
		status          TEXT,
		start_at        INTEGER,
		end_at          INTEGER,
		account_link_url TEXT,
		allowed_channels TEXT,   -- JSON array of channel logins
		details_fetched INTEGER
	);`,

	`CREATE TABLE IF NOT EXISTS drops (
		id               TEXT PRIMARY KEY,
		campaign_id      TEXT NOT NULL,
		name             TEXT,
		required_minutes INTEGER,
		benefits         TEXT,   -- JSON array of {id,name,image_url}
		preconditions    TEXT,   -- JSON array of drop ids
		start_at         INTEGER,
		end_at           INTEGER,
		FOREIGN KEY(campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE
	);`,

	`CREATE TABLE IF NOT EXISTS progress (
		drop_id          TEXT PRIMARY KEY,
		claim_id         TEXT,
		real_minutes     INTEGER,
		extra_minutes    INTEGER,
		required_minutes INTEGER,
		claimable        INTEGER,
		claimed_at       INTEGER,
		source           TEXT,
		observed_at      INTEGER
	);`,

	`CREATE TABLE IF NOT EXISTS queue (
		drop_id         TEXT PRIMARY KEY,
		campaign_id     TEXT,
		position        INTEGER,
		enabled         INTEGER,
		status          TEXT,
		reason          TEXT,
		last_attempt_at INTEGER
	);`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id             TEXT PRIMARY KEY,
		drop_id        TEXT,
		campaign_id    TEXT,
		channel_login  TEXT,
		started_at     INTEGER,
		ended_at       INTEGER,
		start_minutes  INTEGER,
		end_minutes    INTEGER,
		hard_refreshes INTEGER,
		stall_events   INTEGER,
		outcome        TEXT,
		note           TEXT
	);`,

	`CREATE TABLE IF NOT EXISTS claims (
		drop_id     TEXT PRIMARY KEY,
		campaign_id TEXT,
		drop_name   TEXT,
		game_name   TEXT,
		benefits    TEXT,   -- JSON
		claimed_at  INTEGER
	);`,

	`CREATE TABLE IF NOT EXISTS channel_stats (
		login     TEXT PRIMARY KEY,
		successes INTEGER DEFAULT 0,
		failures  INTEGER DEFAULT 0,
		last_seen INTEGER
	);`,

	`CREATE TABLE IF NOT EXISTS blacklist (
		login  TEXT PRIMARY KEY,
		reason TEXT,
		until  INTEGER
	);`,

	`CREATE INDEX IF NOT EXISTS idx_drops_campaign ON drops(campaign_id);`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);`,
	`CREATE INDEX IF NOT EXISTS idx_claims_claimed ON claims(claimed_at);`,
}
