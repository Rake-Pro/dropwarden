// api.js talks to the Go backend over a small JSON API served on the same
// origin as the frontend (see internal/service/httpapi.go). No generated Wails
// bindings are required: this works identically under `go run ./cmd/dropwarden`,
// `wails3 dev`, and a plain browser pointed at the dev server.
//
// If the API is unreachable (e.g. opening index.html directly with no backend),
// it falls back to SAMPLE data so the page still renders for design review.

const SAMPLE = {
  connection: { chrome: false, loggedIn: false },
  status: { state: 'idle', channelLogin: '', dropName: '', current: 0, required: 0, game: '' },
  queue: [
    { dropID: 'd1', dropName: 'Wooden Barricade', game: 'Rust', status: 'active', enabled: true, currentMinutes: 42, requiredMinutes: 60, reason: '@shroud · watching now' },
    { dropID: 'd2', dropName: 'Elite Wooden Door Skin', game: 'Rust', status: 'pending', enabled: true, currentMinutes: 0, requiredMinutes: 120, reason: '@posty +6' },
    { dropID: 'd4', dropName: 'Master Chief Armor Coating', game: 'Halo MCC', status: 'needs_action', enabled: false, currentMinutes: 0, requiredMinutes: 90, reason: 'account link required' },
  ],
  claims: [{ dropName: 'HCS 2026 Weapon Charm', game: 'Halo Infinite', claimedAt: 'sample' }],
  stats: { dropsClaimed: 0, minutesWatched: 0, successRate: 0, sessions: 0, outcomeCounts: {} },
  spark: [],
  config: {
    theme: 'dark', games: ['Rust', 'Halo Infinite', 'Halo MCC', 'Once Human'], anyGame: false,
    priorityMode: 'top_down', autoEnqueueNew: true, volume: 0.01, forceLowestQuality: true,
    neverFocus: true, startMinimized: false, dimOpacity: 0.55, chromePath: '', muteAudio: false, claimStrategy: 'in_page',
    allowGqlFallback: false, skipAccountLink: true, authMode: 'manual', storedEnabled: false, logLevel: 'info',
  },
};

let cache = null;
let cacheAt = 0;
let offline = false;

async function fetchState() {
  const now = Date.now();
  if (cache && now - cacheAt < 800) return cache;
  try {
    const r = await fetch('/api/state', { cache: 'no-store' });
    if (!r.ok) throw new Error('state ' + r.status);
    cache = await r.json();
    cacheAt = now;
    offline = false;
    return cache;
  } catch (e) {
    offline = true;
    return SAMPLE;
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
  async refreshState() { cache = null; return fetchState(); },
  async connection() { return (await fetchState()).connection || {}; },
  async status() { return (await fetchState()).status || {}; },
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

  start() { return post('/api/watch/start'); },
  stop() { return post('/api/watch/stop'); },
  skip() { return post('/api/watch/skip'); },
  refresh() { return post('/api/watch/refresh'); },
  setEnabled(dropID, on) { return post('/api/queue/enabled', { dropID, on }); },
  setOrder(ids) { return post('/api/queue/order', { ids }); },
  setPriority(mode) { return post('/api/queue/priority', { mode }); },

  // Events arrive via polling in app.js; this is a no-op placeholder kept for
  // the call sites.
  onEvent(_name, _cb) {},
};
