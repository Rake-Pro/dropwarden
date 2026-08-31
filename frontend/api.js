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
        platform: 'twitch', enabled: true, up: true, needsLogin: false,
        status: { profile: 'main', platform: 'twitch', state: 'watching', paused: false, channelLogin: 'shroud', dropName: 'Wooden Barricade', current: 42, required: 60, game: 'Rust' },
      },
      {
        // needsLogin sample so the first-run login flow is designable offline
        // (a paused/idle engine parked on a fresh Chrome profile, per the real
        // backend's platformStates in internal/service/httpapi.go).
        platform: 'kick', enabled: true, up: true, needsLogin: true,
        status: { profile: 'main', platform: 'kick', state: 'idle', paused: true, channelLogin: '', dropName: '', current: 0, required: 0, game: '' },
      },
    ],
    queue: [
      { dropID: 'd1', platform: 'twitch', dropName: 'Wooden Barricade', game: 'Rust', status: 'active', enabled: true, currentMinutes: 42, requiredMinutes: 60, reason: '@shroud · watching now' },
      { dropID: 'd2', platform: 'kick', dropName: 'Elite Wooden Door Skin', game: 'Rust', status: 'pending', enabled: true, currentMinutes: 0, requiredMinutes: 120, reason: '@posty +6' },
      { dropID: 'd4', platform: 'twitch', dropName: 'Master Chief Armor Coating', game: 'Halo MCC', status: 'needs_action', enabled: false, currentMinutes: 0, requiredMinutes: 90, reason: 'account link required' },
    ],
    claims: [{ dropName: 'HCS 2026 Weapon Charm', platform: 'twitch', game: 'Halo Infinite', claimedAt: 'sample' }],
    stats: { dropsEarned: 6, dropsClaimed: 5, minutesWatched: 812, successRate: 88, sessions: 14, outcomeCounts: { completed: 12, skipped: 2 }, byPlatform: {
      twitch: { dropsEarned: 4, dropsClaimed: 3, minutesWatched: 560, sessions: 9 },
      kick: { dropsEarned: 2, dropsClaimed: 2, minutesWatched: 252, sessions: 5 },
    } },
    spark: [10, 30, 20, 60, 40, 80, 55, 90, 70, 100, 65, 45, 85, 60],
  }, {
    name: 'p2', displayName: 'Profile 2', enabled: true,
    platforms: [
      {
        platform: 'twitch', enabled: true, up: true, needsLogin: false,
        status: { profile: 'p2', platform: 'twitch', state: 'opening', paused: false, channelLogin: 'ninja', dropName: '', current: 0, required: 0, game: 'Once Human' },
      },
      { platform: 'kick', enabled: false, up: false, needsLogin: false, status: null },
    ],
    queue: [
      { dropID: 'd5', platform: 'twitch', dropName: 'Silver Backpack Skin', game: 'Once Human', status: 'pending', enabled: true, currentMinutes: 0, requiredMinutes: 90, reason: '@ninja · queued' },
    ],
    claims: [],
    stats: { dropsEarned: 1, dropsClaimed: 1, minutesWatched: 96, successRate: 100, sessions: 2, outcomeCounts: { completed: 2 }, byPlatform: {
      twitch: { dropsEarned: 1, dropsClaimed: 1, minutesWatched: 96, sessions: 2 },
    } },
    spark: [0, 0, 5, 10, 0, 0, 20, 15, 0, 30, 10, 0, 0, 5],
  }],
  config: {
    theme: 'dark', games: ['Rust', 'Halo Infinite', 'Halo MCC', 'Once Human'], anyGame: false,
    priorityMode: 'top_down', autoEnqueueNew: true, volume: 0.01, forceLowestQuality: true,
    neverFocus: true, startMinimized: false, dimOpacity: 0.55, chromePath: '', muteAudio: false, kickEnabled: true, claimStrategy: 'in_page',
    allowGqlFallback: false, skipAccountLink: true, authMode: 'manual', storedEnabled: false, logLevel: 'info',
  },
};

// SAMPLE_PROFILE_CONFIGS mirrors GET /api/profiles (the raw per-profile
// overlay the Profiles settings pane edits) for offline preview.
const SAMPLE_PROFILE_CONFIGS = [
  { name: 'main', displayName: 'main', enabled: true, games: [], inheritGames: true, kickEnabled: true },
  { name: 'p2', displayName: 'Profile 2', enabled: true, games: ['Once Human'], inheritGames: false, kickEnabled: false },
];

let cache = null;
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
    return cache;
  } catch (e) {
    offline = true;
    return flatten(SAMPLE);
  }
}

async function post(path, body) {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  cache = null; // invalidate so the next read reflects the action
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || ('request failed: ' + r.status));
  return j;
}

export const api = {
  get offline() { return offline; },
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
      skip() { return post(`${base}/skip`); },
      refresh() { return post(`${base}/refresh`); },
    };
  },

  setEnabled(dropID, on, profile = activeProfile) { return post('/api/queue/enabled', { profile, dropID, on }); },
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
  // distinct from the resolved blocks profiles() returns.
  async profileConfigs() {
    try {
      const r = await fetch('/api/profiles', { cache: 'no-store' });
      if (!r.ok) throw new Error('profiles ' + r.status);
      return await r.json();
    } catch (e) {
      return SAMPLE_PROFILE_CONFIGS;
    }
  },
  // body: { name, displayName, kickEnabled, games, inheritGames }.
  updateProfile(body) { return post('/api/profiles/update', body); },

  // Events arrive via polling in app.js; this is a no-op placeholder kept for
  // the call sites.
  onEvent(_name, _cb) {},
};
