// api.js talks to the Go backend over a small JSON API served on the same
// origin as the frontend (see internal/service/httpapi.go). No generated Wails
// bindings are required: this works identically under `go run ./cmd/dropwarden`,
// `wails3 dev`, and a plain browser pointed at the dev server.
//
// If the API is unreachable (e.g. opening index.html directly with no backend),
// it falls back to SAMPLE data so the page still renders for design review.
// SAMPLE carries two profiles so the unified multi-profile dashboard is
// designable offline; a real single-profile install only ever sends one.

const SAMPLE = {
  profiles: [{
    name: 'main', displayName: 'main', enabled: true,
    platforms: [
      {
        platform: 'twitch', enabled: true, up: true, needsLogin: false, wanted: false, launchError: '', reason: '',
        status: { profile: 'main', platform: 'twitch', state: 'watching', paused: false, channelLogin: 'shroud', dropName: 'Wooden Barricade', current: 42, required: 60, game: 'Rust', reason: '' },
      },
      {
        // needsLogin sample so the first-run login flow is designable offline
        // (a paused/idle engine parked on a fresh Chrome profile, per the real
        // backend's platformStates in internal/service/httpapi.go).
        platform: 'kick', enabled: true, up: true, needsLogin: true, launchError: '', reason: 'paused',
        status: { profile: 'main', platform: 'kick', state: 'idle', paused: true, channelLogin: '', dropName: '', current: 0, required: 0, game: '', reason: 'paused' },
      },
    ],
    queue: [
      { dropID: 'd1', platform: 'twitch', dropName: 'Wooden Barricade', game: 'Rust', position: 1, status: 'active', enabled: true, currentMinutes: 42, requiredMinutes: 60, reason: '@shroud - watching now', needsUser: false, actionURL: '', actionLabel: '' },
      { dropID: 'd2', platform: 'kick', dropName: 'Elite Wooden Door Skin', game: 'Rust', position: 2, status: 'pending', enabled: true, pin: 1, currentMinutes: 0, requiredMinutes: 120, reason: '@posty +6', needsUser: false, actionURL: '', actionLabel: '' },
      { dropID: 'd3', platform: 'twitch', dropName: 'Rust General Crate', game: 'Rust', position: 3, status: 'blocked_precondition', enabled: true, alongside: true, currentMinutes: 10, requiredMinutes: 60, reason: 'waiting on prerequisite drop', needsUser: false, actionURL: '', actionLabel: '' },
      { dropID: 'd7', platform: 'twitch', dropName: 'Halo Weapon Charm', game: 'Halo Infinite', position: 4, status: 'pending', enabled: true, currentMinutes: 5, requiredMinutes: 60, reason: 'any channel', needsUser: false, actionURL: '', actionLabel: '' },
      { dropID: 'd4', platform: 'twitch', dropName: 'Master Chief Armor Coating', game: 'Halo MCC', position: 5, status: 'needs_action', enabled: false, currentMinutes: 0, requiredMinutes: 90, reason: 'account link required', needsUser: true, actionURL: 'https://www.twitch.tv/drops/campaigns', actionLabel: 'Link account' },
      { dropID: 'd6', platform: 'twitch', dropName: 'VOID WINGS SKIN: UNKNOWN', game: 'Once Human', position: 6, status: 'needs_action', enabled: true, owned: true, forceWatch: false, currentMinutes: 0, requiredMinutes: 60, reason: 'reward already owned - Twitch will not credit it again', needsUser: false, actionURL: '', actionLabel: '' },
    ],
    claims: [{ dropName: 'HCS 2026 Weapon Charm', platform: 'twitch', game: 'Halo Infinite', claimedAt: 'sample' }],
    stats: { dropsEarned: 6, dropsClaimed: 5, minutesWatched: 812, successRate: 88, sessions: 14, outcomeCounts: { completed: 12, skipped: 2 }, byPlatform: {
      twitch: { dropsEarned: 4, dropsClaimed: 3, minutesWatched: 560, sessions: 9 },
      kick: { dropsEarned: 2, dropsClaimed: 2, minutesWatched: 252, sessions: 5 },
    } },
    spark: [10, 30, 20, 60, 40, 80, 55, 90, 70, 100, 65, 45, 85, 60],
    inheritGames: true, games: ['Rust', 'Halo Infinite', 'Halo MCC', 'Once Human'],
  }, {
    name: 'p2', displayName: 'Profile 2', enabled: true, inheritGames: false, games: ['Once Human'],
    platforms: [
      {
        platform: 'twitch', enabled: true, up: true, needsLogin: false, launchError: '', reason: 'nothing eligible to watch',
        status: { profile: 'p2', platform: 'twitch', state: 'cooldown', paused: false, channelLogin: '', dropName: '', current: 0, required: 0, game: '', reason: 'nothing eligible to watch' },
      },
      { platform: 'kick', enabled: false, up: false, needsLogin: false, launchError: '', reason: '', status: null },
    ],
    queue: [
      { dropID: 'd5', platform: 'twitch', dropName: 'Silver Backpack Skin', game: 'Once Human', position: 1, status: 'pending', enabled: true, currentMinutes: 0, requiredMinutes: 90, reason: '@ninja - queued', needsUser: false, actionURL: '', actionLabel: '' },
    ],
    claims: [],
    stats: { dropsEarned: 1, dropsClaimed: 1, minutesWatched: 96, successRate: 100, sessions: 2, outcomeCounts: { completed: 2 }, byPlatform: {
      twitch: { dropsEarned: 1, dropsClaimed: 1, minutesWatched: 96, sessions: 2 },
    } },
    spark: [0, 0, 5, 10, 0, 0, 20, 15, 0, 30, 10, 0, 0, 5],
  }],
  config: {
    theme: 'dark', games: ['Rust', 'Halo Infinite', 'Halo MCC', 'Once Human'], anyGame: false, fallThrough: true,
    priorityMode: 'top_down', preferChannelSpecific: true, autoEnqueueNew: true, volume: 0.01, forceLowestQuality: true,
    neverFocus: true, alwaysOnBottom: true, startMinimized: false, dimOpacity: 0.55, chromePath: '', muteAudio: false, kickEnabled: true, claimStrategy: 'in_page',
    allowGqlFallback: false, skipAccountLink: true, claimButtonSelector: 'button:has([data-a-target="tw-core-button-label-text"])', claimButtonText: 'Claim Now', authMode: 'manual', storedEnabled: false, logLevel: 'info', captureGqlBodies: false,
    updateEnabled: true, updateAutoInstall: true, updateShowChangelog: true,
    updateFeedURL: 'https://cdn.rake.pro/releases/dropwarden/latest.json', updateIntervalHours: 24,
  },
  // Mirrors GET /api/update; a pending update so the dashboard banner and the
  // update dialog are designable offline.
  version: '0.1.0',
  host: { os: 'windows', chromeFound: true, chromePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe' },
  update: {
    currentVersion: '0.1.0', enabled: true, autoInstall: true, showChangelog: true, intervalHours: 24,
    feedURL: 'https://cdn.rake.pro/releases/dropwarden/latest.json', devBuild: false,
    status: 'available', phase: 'idle', percent: -1, bytesDone: 0, bytesTotal: 0,
    pending: { version: '0.2.0', notes: [
      { text: 'v0.2.0  (2026-10-01)', heading: true },
      { text: 'Added', heading: true },
      { text: '- Sample release note line' },
    ] },
    lastCheck: null, lastError: '', deferredReason: '', failedVersion: '', failMessage: '', skippedVersion: '', promptSeq: 0,
    installedNotes: [],
  },
};

// SAMPLE_PROFILE_CONFIGS mirrors GET /api/profiles (the raw per-profile
// overlay the Profiles settings pane edits) for offline preview.
const SAMPLE_PROFILE_CONFIGS = [
  { name: 'main', displayName: 'main', enabled: true, games: [], inheritGames: true, kickEnabled: true },
  { name: 'p2', displayName: 'Profile 2', enabled: true, games: ['Once Human'], inheritGames: false, kickEnabled: false },
];

// SAMPLE_CATALOG mirrors GET /api/games/catalog; SAMPLE_SEARCH mirrors
// GET /api/games/search.
const SAMPLE_CATALOG = {
  fetchedAt: '2026-09-26T10:30:00Z', sources: { kick: true, twitchDrops: true },
  games: [
    { game: 'Rust', source: 'campaign', platforms: ['twitch', 'kick'], campaigns: 3, endsAt: '2026-10-12T18:00:00Z', live: 412 },
    { game: 'Marvel Rivals', source: 'twitch_drops', platforms: ['twitch'], campaigns: 0, endsAt: '', live: 96 },
    { game: 'Escape from Tarkov', source: 'kick', platforms: ['kick'], campaigns: 1, endsAt: '2026-10-05T18:00:00Z', live: 0 },
  ],
};
const SAMPLE_SEARCH = [
  { game: 'Rust', id: '263490', boxArt: '' },
  { game: 'Rust Console Edition', id: '1234', boxArt: '' },
];

let cache = null;
// lastGood/everOnline: once the backend has answered, a failed poll (the app
// restarting after an update) keeps showing the last real state instead of
// swapping in SAMPLE, and reports disconnected rather than preview mode.
let lastGood = null;
let everOnline = false;
let disconnected = false;
let cacheAt = 0;
let offline = false;
// activeProfile is the DEFAULT profile action calls target when the caller
// omits one. It always tracks the first profile (the single-profile case);
// the Phase 2 profile selector in app.js lives separately and is passed
// explicitly to every call site that needs a specific profile.
let activeProfile = 'main';

// flatten exposes the FIRST profile's platforms/queue/claims/stats/spark at
// the top level (so single-profile call sites keep reading state the way
// they always have) while leaving the full `profiles` array intact for
// multi-profile-aware call sites.
function flatten(state) {
  const profiles = state.profiles || [];
  const p = profiles[0] || {};
  activeProfile = p.name || activeProfile;
  return {
    ...state,
    profile: p.name || '',
    platforms: p.platforms || [],
    queue: p.queue || [],
    claims: p.claims || [],
    stats: p.stats || {},
    spark: p.spark || [],
  };
}

async function fetchState() {
  const now = Date.now();
  if (cache && now - cacheAt < 800) return cache;
  try {
    const r = await fetch('/api/state', { cache: 'no-store' });
    if (!r.ok) throw new Error('state ' + r.status);
    cache = flatten(await r.json());
    cacheAt = now;
    offline = false;
    everOnline = true;
    disconnected = false;
    lastGood = cache;
    return cache;
  } catch (e) {
    if (everOnline && lastGood) { disconnected = true; return lastGood; }
    offline = true;
    return flatten(SAMPLE);
  }
}

// A fetch that never reached the backend throws the browser's own TypeError
// ("Failed to fetch", "Load failed"); actions show this instead.
const NOT_RESPONDING = "dropwarden isn't responding. Try again in a moment.";

async function post(path, body) {
  let r;
  try {
    r = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch (_) {
    throw new Error(NOT_RESPONDING);
  }
  cache = null; // invalidate so the next read reflects the action
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    // {error, field?}: field names the config field a validation error is about.
    const err = new Error(j.error || ('request failed: ' + r.status));
    if (j.field) err.field = j.field;
    throw err;
  }
  return j;
}

async function getText(path) {
  let r;
  try { r = await fetch(path, { cache: 'no-store' }); } catch (_) { throw new Error(NOT_RESPONDING); }
  if (!r.ok) throw new Error('request failed: ' + r.status);
  return r.text();
}

export const api = {
  get offline() { return offline; },
  // True when the backend answered before but not now (e.g. restarting).
  get disconnected() { return disconnected; },
  // The profile action calls target when none is passed explicitly.
  get profile() { return activeProfile; },
  async state() { return fetchState(); },
  async refreshState() { cache = null; return fetchState(); },
  // Every profile's full block (name, displayName, enabled, platforms, queue,
  // claims, stats, spark), in config order.
  async profiles() { return (await fetchState()).profiles || []; },
  // Enabled/up/status per platform of the first profile, twitch first.
  async platforms() { return (await fetchState()).platforms || []; },
  async queue() { return (await fetchState()).queue || []; },
  async claims() { return (await fetchState()).claims || []; },
  async stats() { return (await fetchState()).stats || {}; },
  async spark() { return (await fetchState()).spark || []; },
  async config() { return (await fetchState()).config || {}; },
  async saveConfig(cfg) { return post('/api/config', cfg); },
  async debug() {
    const r = await fetch('/api/debug', { cache: 'no-store' });
    if (!r.ok) throw new Error('debug ' + r.status);
    return r.json();
  },

  // Engine controls, uniform across profiles and platforms.
  engine(platform, profile = activeProfile) {
    const base = `/api/engine/${encodeURIComponent(profile)}/${encodeURIComponent(platform)}`;
    return {
      start() { return post(`${base}/start`); },
      // Launches the watcher window parked on the login page (no watcher yet).
      login() { return post(`${base}/login`); },
      stop() { return post(`${base}/stop`); },
      // dropID: the drop the caller showed; the server skips only if that
      // drop is still the one being watched.
      skip(dropID) { return post(`${base}/skip`, dropID ? { dropID } : undefined); },
      refresh() { return post(`${base}/refresh`); },
      // Parks a running engine with its window open (the tray's Pause).
      pause() { return post(`${base}/pause`); },
    };
  },

  setEnabled(dropID, on, profile = activeProfile) { return post('/api/queue/enabled', { profile, dropID, on }); },
  setForceWatch(dropID, on, profile = activeProfile) { return post('/api/queue/force-watch', { profile, dropID, on }); },
  // Star (pinned=true) or unstar a drop; starred drops are watched first.
  setPin(dropID, pinned, profile = activeProfile) { return post('/api/queue/pin', { profile, dropID, pinned }); },
  // Move a pinned drop one rank: direction 'up' (toward rank 1) or 'down'.
  setPinRank(dropID, direction, profile = activeProfile) { return post('/api/queue/pin-rank', { profile, dropID, direction }); },
  setOrder(ids, profile = activeProfile) { return post('/api/queue/order', { profile, ids }); },
  // Priority mode is shared by every profile.
  setPriority(mode) { return post('/api/queue/priority', { mode }); },
  addProfile(name, displayName) { return post('/api/profiles', { name, displayName }); },
  setProfileEnabled(name, on) { return post('/api/profiles/enabled', { name, on }); },
  // Never deletes the data directory; the response carries its path so the UI
  // can tell the user where it still lives.
  removeProfile(name) { return post('/api/profiles/remove', { name }); },
  // Returns { loggedIn: true|false|null }; null means unknowable (no session
  // up, or a platform with no probe - see docs/NOTES-kick.md). On-demand only.
  loginCheck(profile, platform) { return post('/api/profiles/login-check', { profile, platform }); },
  // The Profiles settings pane's raw per-profile overlay (games/inherit/kick),
  // distinct from the resolved blocks profiles() returns. Like update(), it
  // throws once the backend has been seen: SAMPLE only before first contact.
  async profileConfigs() {
    try {
      const r = await fetch('/api/profiles', { cache: 'no-store' });
      if (!r.ok) throw new Error('profiles ' + r.status);
      return await r.json();
    } catch (e) {
      if (everOnline) throw (e instanceof TypeError ? new Error(NOT_RESPONDING) : e);
      return SAMPLE_PROFILE_CONFIGS;
    }
  },
  // body: { name, displayName, kickEnabled, games, inheritGames }.
  updateProfile(body) { return post('/api/profiles/update', body); },

  // Self-updater. update() is cheap (no network); checkUpdate() runs a real
  // feed check and returns the resulting state; installUpdate() starts the
  // install and returns the new state (the app quits and restarts when done).
  // update() throws once the backend has been seen (so the progress poll can
  // tell "restarting" from "no backend"); SAMPLE only before first contact.
  async update() {
    try {
      const r = await fetch('/api/update', { cache: 'no-store' });
      if (!r.ok) throw new Error('update ' + r.status);
      return await r.json();
    } catch (e) {
      if (everOnline) throw (e instanceof TypeError ? new Error(NOT_RESPONDING) : e);
      return SAMPLE.update;
    }
  },
  checkUpdate() { return post('/api/update/check'); },
  installUpdate() { return post('/api/update/install'); },

  // Game picker: the merged "drops right now" catalogue (fetched on demand;
  // refresh=true asks the server to bypass its ~10 min cache) and Twitch's
  // category search.
  async gamesCatalog(refresh = false) {
    try {
      const r = await fetch('/api/games/catalog' + (refresh ? '?refresh=1' : ''), { cache: 'no-store' });
      if (!r.ok) throw new Error('catalog ' + r.status);
      return await r.json();
    } catch (e) {
      if (everOnline) throw (e instanceof TypeError ? new Error(NOT_RESPONDING) : e);
      return SAMPLE_CATALOG;
    }
  },
  async searchGames(q) {
    try {
      const r = await fetch('/api/games/search?q=' + encodeURIComponent(q), { cache: 'no-store' });
      if (!r.ok) throw new Error('search ' + r.status);
      return (await r.json()) || [];
    } catch (e) {
      if (everOnline) throw (e instanceof TypeError ? new Error(NOT_RESPONDING) : e);
      return SAMPLE_SEARCH.filter(x => x.game.toLowerCase().includes(String(q).toLowerCase()));
    }
  },
  // Opens url in that profile's logged-in watcher Chrome (system browser fallback).
  open(profile, platform, url) { return post('/api/open', { profile, platform, url }); },
  // Plain-text diagnostics bundle for the clipboard.
  diagnostics() { return getText('/api/diagnostics'); },
  // Writes a diagnostics zip to the Desktop; returns { path }.
  exportDiagnostics() { return post('/api/diagnostics/export'); },

  // Events arrive via polling in app.js; this is a no-op placeholder kept for
  // the call sites.
  onEvent(_name, _cb) {},
};
