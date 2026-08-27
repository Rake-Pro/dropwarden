// api.js is the single integration seam between the UI and the Go backend.
//
// LIVE WIRING (done once on your machine after `wails3 generate bindings`):
//   The Taskfile generates typed JS bindings under ./bindings/. To go live,
//   set window.dw in a tiny bridge (or here) to the generated services, e.g.:
//
//     import {CampaignService, QueueService, WatcherService,
//             HistoryService, SettingsService} from './bindings/.../service';
//     window.dw = {CampaignService, QueueService, WatcherService,
//                  HistoryService, SettingsService};
//
//   and subscribe to events via '@wailsio/runtime' Events.On(name, cb).
//
// Until window.dw is present, the UI renders from SAMPLE data so the page is
// fully viewable in a plain browser (go run / wails3 dev before bindings exist).

const LIVE = typeof window !== 'undefined' && !!window.dw;

const SAMPLE = {
  connection: { chrome: true, loggedIn: true, user: 'you', nextRefreshMin: 34 },
  status: {
    state: 'watching', channelLogin: 'shroud', dropName: 'Rust Twitch Drops — Wooden Barricade',
    current: 42, required: 60, game: 'Rust', viewers: 18240,
  },
  queue: [
    { dropID: 'd1', dropName: 'Wooden Barricade', game: 'Rust', status: 'active', enabled: true, currentMinutes: 42, requiredMinutes: 60, reason: '@shroud · watching now' },
    { dropID: 'd2', dropName: 'Elite Wooden Door Skin', game: 'Rust', status: 'pending', enabled: true, currentMinutes: 0, requiredMinutes: 120, reason: '@posty, @spoonkid +6' },
    { dropID: 'd3', dropName: 'Cradle Whisperer Bundle', game: 'Once Human', status: 'pending', enabled: true, currentMinutes: 40, requiredMinutes: 180, reason: 'any drops channel' },
    { dropID: 'd4', dropName: 'Master Chief Armor Coating', game: 'Halo MCC', status: 'needs_action', enabled: false, currentMinutes: 0, requiredMinutes: 90, reason: 'account link required' },
    { dropID: 'd5', dropName: 'Tier 2 — Reinforced Door', game: 'Rust', status: 'blocked', enabled: true, currentMinutes: 0, requiredMinutes: 120, reason: 'waiting on prerequisite drop' },
    { dropID: 'd6', dropName: 'HCS 2026 Weapon Charm', game: 'Halo Infinite', status: 'claimed', enabled: false, currentMinutes: 60, requiredMinutes: 60, reason: '' },
  ],
  claims: [
    { dropName: 'HCS 2026 Weapon Charm', game: 'Halo Infinite', claimedAt: 'Today · 11:20' },
    { dropName: 'Nightfall Rifle Skin', game: 'Once Human', claimedAt: 'Today · 09:04' },
    { dropName: 'Wooden Window Bars', game: 'Rust', claimedAt: 'Today · 07:48' },
    { dropName: 'Reinforced Barricade', game: 'Rust', claimedAt: 'Yesterday · 23:31' },
  ],
  stats: { dropsClaimed: 27, minutesWatched: 1884, successRate: 92, sessions: 34,
    outcomeCounts: { claimed: 27, completed: 2, aborted_offline: 3, aborted_raid: 2 } },
  spark: [40,62,31,78,55,90,48,70,35,82,60,95,52,68],
  config: {
    theme: 'dark', games: ['Rust','Halo Infinite','Halo MCC','Once Human'], anyGame: false,
    priorityMode: 'top_down', autoEnqueueNew: true, volume: 0.01, forceLowestQuality: true,
    neverFocus: true, dimOpacity: 0.55, chromePath: '', claimStrategy: 'in_page',
    allowGqlFallback: false, skipAccountLink: true, authMode: 'manual', storedEnabled: false, logLevel: 'info',
  },
};

async function svc(service, method, ...args) {
  if (LIVE && window.dw[service] && window.dw[service][method]) {
    return await window.dw[service][method](...args);
  }
  return null;
}

export const api = {
  live: LIVE,
  async connection() { return SAMPLE.connection; },
  async status() { return (await svc('WatcherService', 'Status')) ?? SAMPLE.status; },
  async queue() { return (await svc('QueueService', 'Get')) ?? SAMPLE.queue; },
  async claims() { return (await svc('HistoryService', 'Claims')) ?? SAMPLE.claims; },
  async stats() { return (await svc('HistoryService', 'Stats')) ?? SAMPLE.stats; },
  async spark() { return SAMPLE.spark; },
  async config() { return (await svc('SettingsService', 'Load')) ?? SAMPLE.config; },
  async saveConfig(cfg) { return svc('SettingsService', 'Save', cfg); },

  start() { return svc('WatcherService', 'Start'); },
  stop() { return svc('WatcherService', 'Stop'); },
  skip() { return svc('WatcherService', 'SkipCurrent'); },
  refresh() { return svc('WatcherService', 'ForceRefresh'); },
  setEnabled(dropID, on) { return svc('QueueService', 'SetEnabled', dropID, on); },
  setOrder(ids) { return svc('QueueService', 'SetOrder', ids); },
  setPriority(mode) { return svc('QueueService', 'SetPriorityMode', mode); },

  // Event subscription seam. Live: wire to @wailsio/runtime Events.On.
  onEvent(_name, _cb) { /* no-op until bindings are wired */ },
};
