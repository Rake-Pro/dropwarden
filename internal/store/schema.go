package store

// schemaVersion is bumped whenever migrations change. Migrations are applied
// in order on Open; each is idempotent via IF NOT EXISTS so re-running is safe.
const schemaVersion = 3

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

	// account_connected mirrors Twitch's self.isAccountConnected (NULL = not yet
	// captured). Not idempotent by itself (SQLite has no ADD COLUMN IF NOT
	// EXISTS), but the version gate runs it exactly once. New migrations must
	// only ever be APPENDED here - the version pointer of an existing database
	// is already past every earlier index.
	`ALTER TABLE campaigns ADD COLUMN account_connected INTEGER;`,

	// Multi-platform support: a platform discriminator on every table whose
	// rows belong to one platform. Existing rows are all Twitch. channel_stats
	// and blacklist are keyed by login, which can collide across platforms, so
	// they are rebuilt with a composite (platform, login) primary key - the
	// rename/create/copy/drop steps are separate entries because migrations
	// run one statement per entry.
	`ALTER TABLE campaigns ADD COLUMN platform TEXT NOT NULL DEFAULT 'twitch';`,
	`ALTER TABLE queue ADD COLUMN platform TEXT NOT NULL DEFAULT 'twitch';`,
	`ALTER TABLE sessions ADD COLUMN platform TEXT NOT NULL DEFAULT 'twitch';`,
	`ALTER TABLE claims ADD COLUMN platform TEXT NOT NULL DEFAULT 'twitch';`,
	`ALTER TABLE channel_stats RENAME TO channel_stats_old;`,
	`CREATE TABLE IF NOT EXISTS channel_stats (
		platform  TEXT NOT NULL DEFAULT 'twitch',
		login     TEXT NOT NULL,
		successes INTEGER DEFAULT 0,
		failures  INTEGER DEFAULT 0,
		last_seen INTEGER,
		PRIMARY KEY (platform, login)
	);`,
	`INSERT INTO channel_stats (platform, login, successes, failures, last_seen)
		SELECT 'twitch', login, successes, failures, last_seen FROM channel_stats_old;`,
	`DROP TABLE channel_stats_old;`,
	`ALTER TABLE blacklist RENAME TO blacklist_old;`,
	`CREATE TABLE IF NOT EXISTS blacklist (
		platform TEXT NOT NULL DEFAULT 'twitch',
		login    TEXT NOT NULL,
		reason   TEXT,
		until    INTEGER,
		PRIMARY KEY (platform, login)
	);`,
	`INSERT INTO blacklist (platform, login, reason, until)
		SELECT 'twitch', login, reason, until FROM blacklist_old;`,
	`DROP TABLE blacklist_old;`,

	// owned marks a drop whose reward the account already holds (matched at
	// discovery against the earned/owned inventory). Twitch reports such a drop
	// isClaimed=false yet never credits it, so eligibility excludes it before
	// watching. NULL/0 = not owned. Append-only: the version pointer of an
	// existing database is already past every earlier index.
	`ALTER TABLE progress ADD COLUMN owned INTEGER;`,

	// force_watch is a per-drop manual override that makes eligibility ignore the
	// owned-reward skip for that drop (user insists on watching an already-owned
	// reward, e.g. to test crediting). NULL/0 = off. Append-only.
	`ALTER TABLE queue ADD COLUMN force_watch INTEGER;`,
}
