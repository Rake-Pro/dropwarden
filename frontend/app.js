import { api } from '/api.js';

const $ = (s, r = document) => r.querySelector(s);
const el = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const pct = (cur, req) => req > 0 ? Math.min(100, Math.round(cur / req * 100)) : 0;
// fmtMins is "watched / required min" (HTML), never above required; a dash
// when the drop has no watch-time requirement.
const fmtMins = (cur, req) => req > 0 ? `${Math.min(cur || 0, req)} / ${req} min` : '&mdash;';
// fmtWatch is total watch time: minutes under an hour, hours above.
const fmtWatch = (m) => (m || 0) < 60 ? `${m || 0} min` : `${((m || 0) / 60).toFixed(1)} h`;
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// localStorage can throw (private window, blocked site data); every access is
// wrapped and the UI works without it.
const LS = {
  get(k) { try { return localStorage.getItem(k); } catch (_) { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch (_) { /* optional */ } },
};

let cfg = null;
// cfgSnapshot is the saved config as JSON: Settings compares cfg against it
// for dirty tracking, and the Queue orders game sections by its games list.
let cfgSnapshot = '';
// hostInfo is /api/state host: { os, chromeFound, chromePath, keepAwake,
// remote (headless service: offer remote login), updates (self-updater runs) }.
let hostInfo = {};

// selectedProfile drives the Queue/History profile pill bar: a module-level
// var defaulting to the first (enabled) profile, no persistence (see
// pickProfile). The Dashboard shows every enabled profile at once.
let selectedProfile = null;

// ---- icons (inline SVG, decorative, aria-hidden) ----

const ICON_PATHS = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  queue: '<path d="M4 6h16M4 12h16M4 18h10"/>',
  gift: '<rect x="3" y="8" width="18" height="4" rx="1"/><path d="M5 12v8h14v-8M12 8v12M12 8c-1.5-3-5-4-5-1.5S10 8 12 8zm0 0c1.5-3 5-4 5-1.5S14 8 12 8z"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M2 12h3M19 12h3M4.9 19.1L7 17M17 7l2.1-2.1"/>',
  bug: '<rect x="7" y="8" width="10" height="12" rx="5"/><path d="M12 8V5M9 5L8 3M15 5l1-2M3 13h4M17 13h4M4 19l3-2M20 19l-3-2M4 7l3 2M20 7l-3 2"/>',
  up: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  alert: '<path d="M12 3l10 18H2z"/><path d="M12 10v5M12 18v.5"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7"/>',
  circle: '<circle cx="12" cy="12" r="8"/>',
  wait: '<circle cx="12" cy="12" r="8" stroke-dasharray="3 3"/>',
  games: '<rect x="2" y="7" width="20" height="11" rx="5"/><path d="M7 11v3M5.5 12.5h3M15 12h.01M18 13.5h.01"/>',
  platforms: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>',
  player: '<path d="M8 5v14l11-7z"/>',
  browser: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M7 6.5h.01M10 6.5h.01"/>',
  app: '<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 0 0 18z" fill="currentColor"/>',
  profiles: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6 6 0 0 1 3.5 6"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5v.5"/>',
};
const icon = (n, size = 14) => `<svg class="svg-ic" viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${ICON_PATHS[n] || ''}</svg>`;

// ---- labels: every user-facing state/status/outcome goes through these ----

const STATE_LABEL = {
  idle: 'Waiting', discovering: 'Checking drops', selecting_channel: 'Finding a channel',
  opening: 'Starting stream', validating_playback: 'Starting stream', watching: 'Watching',
  claiming: 'Claiming', cooldown: 'Waiting', backoff: 'Retrying shortly', needs_user_action: 'Needs you',
  offline: 'Stopped', paused: 'Paused',
};
const STATUS_LABEL = {
  pending: 'Waiting', active: 'Watching', blocked_precondition: 'Waiting on earlier drop',
  needs_action: 'Needs you', claimed: 'Claimed', expired: 'Expired', unavailable: 'Not available yet', disabled: 'Off',
};
const OUTCOME_LABEL = {
  claimed: 'Claimed', completed: 'Earned', skipped: 'Skipped', aborted_raid: 'Raided away',
  aborted_offline: 'Streamer went offline', aborted_stall: 'Stream stalled', aborted_channel_ended: 'Stream ended',
  crashed: 'Tab crashed', user_stop: 'Stopped by you', error: 'Error',
};
const labelOf = (table, k) => table[k] || cap(k);

// Platform display metadata. Everything platform-facing renders through this
// table so a future platform is one row here, not a scatter of special cases.
const PLATFORM_META = {
  twitch: { label: 'Twitch' },
  kick: { label: 'Kick' },
};
const platLabel = (p) => (PLATFORM_META[p] || { label: p }).label;
const pfBadge = (p) => PLATFORM_META[p] ? `<span class="pf ${esc(p)}">${esc(platLabel(p).toUpperCase())}</span>` : '';
const profBadge = (name) => `<span class="pf profile">${esc(name)}</span>`;

// barHTML is an accessible progress bar (role=progressbar).
function barHTML(cur, req, cls, label) {
  const v = pct(cur, req);
  return `<div class="bar" role="progressbar" aria-label="${esc(label)}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${v}"><i class="${cls}" style="width:${v}%"></i></div>`;
}

// pickProfile resolves the profile the Queue/History views render: enabled
// profiles only, keyed by selectedProfile (falling back to the first).
function pickProfile(state) {
  const profiles = (state.profiles || []).filter(p => p.enabled);
  if (!profiles.length) return { profiles, profile: null, multiProfile: false };
  if (!selectedProfile || !profiles.some(p => p.name === selectedProfile)) selectedProfile = profiles[0].name;
  const profile = profiles.find(p => p.name === selectedProfile) || profiles[0];
  return { profiles, profile, multiProfile: profiles.length > 1 };
}

function profilePillBar(profiles, onPick) {
  const bar = el(`<div class="prof-pills" role="group" aria-label="Profile"></div>`);
  profiles.forEach(p => {
    const on = p.name === selectedProfile;
    const pill = el(`<button type="button" class="prof-pill ${on ? 'active' : ''}" aria-pressed="${on}">${esc(p.displayName)}</button>`);
    pill.addEventListener('click', () => { selectedProfile = p.name; onPick(); });
    bar.append(pill);
  });
  return bar;
}

// A platform is shown when it is enabled OR its engine is still running (just
// disabled, still winding down) - a running engine never loses its controls.
const visiblePlatforms = (plats) => (plats || []).filter(p => p.enabled || p.up);
const isMulti = (plats) => visiblePlatforms(plats).length > 1;

// loadCfg returns a mutable copy of the config so form edits never leak into
// the shared state cache.
async function loadCfg() {
  return { ...(await api.config()) };
}
const takeSnapshot = () => { cfgSnapshot = JSON.stringify(cfg || {}); };
const snap = () => JSON.parse(cfgSnapshot || '{}');

// applyTheme sets the page theme. remember (load and save, never the live
// preview) keeps it for the next start, so the first paint is already right.
function applyTheme(theme, remember = false) {
  if (remember) LS.set('dw.theme', theme || '');
  if (theme && theme !== 'system') document.documentElement.setAttribute('data-theme', theme);
  else document.documentElement.removeAttribute('data-theme');
}

function switchView(view) {
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.dataset.view === view));
  document.querySelectorAll('.nav').forEach(n => {
    const on = n.dataset.view === view;
    n.classList.toggle('active', on);
    if (on) n.setAttribute('aria-current', 'page'); else n.removeAttribute('aria-current');
  });
}

// ---- toasts (action results) ----

const clean = (s) => String(s || '').replace(/^update:\s*/i, '');
const errText = (e) => clean(e && e.message ? e.message : e);

function toast(msg, kind = 'ok') {
  const host = $('#toasts');
  if (!host || !msg) return;
  const t = el(`<div class="toast ${kind}"></div>`);
  t.textContent = clean(msg);
  host.append(t);
  setTimeout(() => t.remove(), kind === 'err' ? 7000 : 3500);
}

// ---- modal dialogs (decisions) ----

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea, summary, [tabindex]:not([tabindex="-1"])';

// focusKey/restoreFocus keep keyboard focus across a rebuild of host: the key
// is the nearest [data-fk] block plus the control's id, aria-label, data-act
// or text, so the same control in the rebuilt DOM gets focus back. When that
// control is now disabled (a row moved to the top), focus goes to the first
// enabled control of the same block.
const ctlLabel = (n) => n.id || n.getAttribute('aria-label') || (n.dataset && n.dataset.act) || n.textContent.trim();
function focusKey(host) {
  const a = document.activeElement;
  if (!host || !a || a === document.body || !host.contains(a)) return null;
  const s = a.closest('[data-fk]');
  return { scope: s && host.contains(s) ? s.dataset.fk : null, tag: a.tagName, label: ctlLabel(a) };
}
function restoreFocus(host, k) {
  if (!host || !k) return;
  const cur = document.activeElement;
  if (cur && cur !== document.body && cur.isConnected) return;
  const scopes = k.scope === null ? [host] : [...host.querySelectorAll('[data-fk]')].filter(s => s.dataset.fk === k.scope);
  for (const s of scopes) {
    const hit = [s, ...s.querySelectorAll(k.tag)].find(n => n.tagName === k.tag && !n.disabled && ctlLabel(n) === k.label);
    const alt = hit || (k.scope !== null && (s.matches(FOCUSABLE) ? s : s.querySelector(FOCUSABLE)));
    if (alt) { alt.focus({ preventScroll: true }); return; }
  }
}
let modalClose = null;
let modalSeq = 0;

function trapTab(e, dlg) {
  const f = [...dlg.querySelectorAll(FOCUSABLE)].filter(x => x.offsetParent !== null);
  if (!f.length) { e.preventDefault(); return; }
  const first = f[0], last = f[f.length - 1];
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
}

// openModal shows dlg (its first h3 labels it) on document.body so the 5 s
// re-renders never close it: initial focus on [data-autofocus] or the first
// control, Tab trapped inside, Escape and backdrop close, focus returns to
// the opener. Returns close(); onClose runs however it closes.
function openModal(dlg, onClose) {
  if (modalClose) modalClose();
  const prev = document.activeElement;
  const back = el('<div class="modal-back"></div>');
  dlg.classList.add('modal', 'card');
  dlg.setAttribute('role', 'dialog');
  dlg.setAttribute('aria-modal', 'true');
  if (!dlg.hasAttribute('tabindex')) dlg.setAttribute('tabindex', '-1');
  const h = dlg.querySelector('h3');
  if (h) { h.id = h.id || 'modal-title-' + (++modalSeq); dlg.setAttribute('aria-labelledby', h.id); }
  back.append(dlg);
  const onKey = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); close(); }
    else if (e.key === 'Tab') trapTab(e, dlg);
  };
  const close = () => {
    if (!back.isConnected) return;
    back.remove();
    document.removeEventListener('keydown', onKey, true);
    if (modalClose === close) modalClose = null;
    if (prev && prev.isConnected && prev.focus) prev.focus();
    if (onClose) onClose();
  };
  back.addEventListener('mousedown', e => { if (e.target === back) close(); });
  document.addEventListener('keydown', onKey, true);
  document.body.append(back);
  modalClose = close;
  const first = dlg.querySelector('[data-autofocus]:not([disabled])') || dlg.querySelector(FOCUSABLE);
  (first || dlg).focus();
  return close;
}

// ---- rail footer ----

function footRow(row, displayName, multiProfile, p) {
  const label = multiProfile ? `${displayName} - ${platLabel(p.platform)}` : platLabel(p.platform);
  if (!p.up) {
    if (hostInfo.chromeFound === false || p.launchError) return row('off', label + ': Chrome problem');
    if (p.wanted) return row('warn', label + ' starting');
    return p.needsLogin ? row('warn', label + ' needs sign-in') : row('idle', label + ' stopped');
  }
  const st = p.status || {};
  if (p.needsLogin) return row('warn', label + ' needs sign-in');
  if (st.paused) return row('warn', label + ' paused');
  return row('ok', label + ' ' + labelOf(STATE_LABEL, st.state || 'idle').toLowerCase());
}

async function renderFoot() {
  const state = await api.state();
  const profiles = state.profiles || [];
  // Same rule as the dashboard: a disabled profile with nothing running is
  // not shown, so it does not count.
  const multiProfile = profiles.filter(p => p.enabled || (p.platforms || []).some(pl => pl.up)).length > 1;
  const foot = $('#rail-foot');
  const row = (cls, label) => `<div class="frow" title="${esc(label)}"><span class="fdot ${cls}"></span><span class="ftxt">${esc(label)}</span></div>`;
  if (api.offline) { foot.innerHTML = row('off', 'Preview mode') + row('off', 'Backend not connected'); return; }
  if (api.disconnected) { foot.innerHTML = row('warn', 'Reconnecting...'); return; }
  let rows = '';
  for (const prof of profiles) {
    for (const p of visiblePlatforms(prof.platforms)) rows += footRow(row, prof.displayName, multiProfile, p);
  }
  if (state.host && state.host.keepAwake) rows += row('', 'Keeping PC awake');
  foot.innerHTML = rows || row('warn', 'No profiles configured');
}

// ---- self-updater UI ----

let updState = null;
let lastPromptSeq = null;
let updPolling = false;
let startVersion = '';
const ACTIVE_PHASES = ['downloading', 'verifying', 'installing', 'handing_off', 'restarting'];
const updBusy = (u) => !!u && ACTIVE_PHASES.includes(u.phase);
const mb = (n) => ((n || 0) / 1048576).toFixed(1) + ' MB';

// onUpdate takes every fresh update snapshot (state poll, /api/update poll,
// check and install responses): it opens the dialog when the tray bumped
// promptSeq, repaints progress in place and starts the fast poll.
function onUpdate(u) {
  if (!u) return;
  updState = u;
  // Only a real backend's version counts: SAMPLE (preview) must not arm the
  // reload-on-new-version check below.
  if (!startVersion && !api.offline) startVersion = u.currentVersion || '';
  // A backend on a new version (the app restarted after an update, however
  // long it took) means this page's code is stale: reload once.
  if (startVersion && u.currentVersion && u.currentVersion !== startVersion && !api.offline) {
    if (reloadOnce(u.currentVersion)) return 'reload';
    startVersion = u.currentVersion;
  }
  const seq = u.promptSeq || 0;
  if (lastPromptSeq === null || hostInfo.updates === false) lastPromptSeq = seq;
  else if (seq !== lastPromptSeq) { lastPromptSeq = seq; showUpdateModal(u); }
  renderUpdateProgress(u);
  syncUpdateBanner(u);
  refreshUpdStatus(u);
  if (u.phase && u.phase !== 'idle' && !updPolling) pollUpdate();
}

// reloadOnce reloads the page for a new backend version at most once per
// version (sessionStorage guard), so a backend that keeps reporting a
// different version cannot loop. Without sessionStorage it never reloads.
function reloadOnce(ver) {
  const k = 'dw.reloadedFor';
  let prev = null;
  try { prev = sessionStorage.getItem(k); } catch (_) { /* optional */ }
  if (prev === ver) return false;
  try { sessionStorage.setItem(k, ver); } catch (_) { return false; }
  location.reload();
  return true;
}

// pollUpdate fetches /api/update every 500 ms while an update runs. A failed
// fetch during hand-off means the app is restarting; the reconnect with a new
// version reloads the page (onUpdate).
async function pollUpdate() {
  updPolling = true;
  let fails = 0;
  for (let i = 0; fails < 240; i++) {
    await sleep(500);
    let u;
    try { u = await api.update(); fails = 0; }
    catch (_) {
      fails++;
      if (updState && updState.phase !== 'idle') renderUpdateProgress({ ...updState, phase: 'restarting' });
      continue;
    }
    if (onUpdate(u) === 'reload') return;
    if ((!u.phase || u.phase === 'idle') && i >= 3) break;
  }
  updPolling = false;
}

function updProgressText(u) {
  const v = u.pending ? 'v' + u.pending.version : 'the update';
  switch (u.phase) {
    case 'checking': return 'Checking for updates...';
    case 'downloading': {
      if (!(u.bytesTotal > 0)) return `Downloading ${v}...` + (u.bytesDone ? ' ' + mb(u.bytesDone) : '');
      return `Downloading ${v}... ${mb(u.bytesDone)} of ${mb(u.bytesTotal)} (${Math.round(updPercent(u))}%)`;
    }
    case 'verifying': return 'Verifying...';
    case 'installing': return 'Installing...';
    case 'handing_off': case 'restarting': return 'Restarting Dropwarden...';
    default: return '';
  }
}

// updPercent prefers the byte counts; percent (0-100, -1 unknown) otherwise.
function updPercent(u) {
  if (u.bytesTotal > 0) return Math.min(100, u.bytesDone / u.bytesTotal * 100);
  return u.percent >= 0 ? Math.min(100, u.percent) : -1;
}

const updProgressHTML = () => `<div class="upd-progress" hidden><div class="bar" role="progressbar" aria-label="Update progress" aria-valuemin="0" aria-valuemax="100"><i></i></div><div class="upd-ptext" aria-live="polite"></div></div>`;

// renderUpdateProgress mutates every .upd-progress node in place (banner,
// Updates pane, dialog), so re-renders around them never reset the bar.
function renderUpdateProgress(u) {
  const show = updBusy(u);
  const text = show ? updProgressText(u) : '';
  const p = u.phase === 'downloading' ? updPercent(u) : -1;
  document.querySelectorAll('.upd-progress').forEach(n => {
    n.hidden = !show;
    const bar = n.querySelector('.bar');
    bar.classList.toggle('indet', show && p < 0);
    if (p >= 0) bar.setAttribute('aria-valuenow', String(Math.round(p))); else bar.removeAttribute('aria-valuenow');
    bar.firstElementChild.style.width = p >= 0 ? p + '%' : '';
    const t = n.querySelector('.upd-ptext');
    if (t.textContent !== text) t.textContent = text;
  });
}

const failedPending = (u) => !!(u && u.pending && u.failedVersion && u.failedVersion === u.pending.version);
const hasNotes = (u) => !!(u && u.pending && u.showChangelog !== false && (u.pending.notes || []).length);

// updBannerModel decides the dashboard banner copy for an update snapshot.
function updBannerModel(u) {
  const p = u && u.pending;
  if (!u || u.devBuild || hostInfo.updates === false) return { show: false };
  if (updBusy(u)) return { show: true, title: `Updating to ${p ? 'v' + p.version : 'the new version'}`, detail: 'Dropwarden restarts by itself when this finishes.' };
  if (!p) return { show: false };
  const notes = hasNotes(u);
  if (failedPending(u)) return { show: true, err: true, title: `Couldn't install v${p.version}`, detail: clean(u.failMessage) || 'The install did not finish.', notes, install: 'Retry install' };
  if (u.deferredReason) return { show: true, title: `v${p.version} is ready`, detail: `It installs automatically when the current watch ends (${u.deferredReason}).`, notes, install: 'Install now' };
  return { show: true, title: `Dropwarden v${p.version} is available`, detail: `You are on v${u.currentVersion}.`, notes, install: 'Install now' };
}

// The dashboard update banner is ONE persistent node: renderDashboard re-appends
// it, and only its text/buttons change (and only when they differ), so the
// progress bar inside it survives the 5 s rebuild.
let updBanner = null;
let updBannerKey = '';

function syncUpdateBanner(u) {
  if (!updBanner) {
    updBanner = el(`<div class="banner update"><span class="ic">${icon('up', 16)}</span><div class="tx"><div class="t"></div><div class="d"></div>${updProgressHTML()}</div><div class="acts"></div></div>`);
    updBanner.querySelector('.acts').addEventListener('click', (e) => {
      const b = e.target.closest('[data-upd]');
      if (!b) return;
      if (b.dataset.upd === 'notes') showUpdateModal(updState);
      else startInstall(b);
    });
  }
  const m = updBannerModel(u);
  updBanner.hidden = !m.show;
  const key = JSON.stringify(m) + api.offline;
  if (key === updBannerKey) return;
  updBannerKey = key;
  updBanner.classList.toggle('err', !!m.err);
  updBanner.querySelector('.t').textContent = m.title || '';
  updBanner.querySelector('.d').textContent = m.detail || '';
  const off = api.offline ? 'disabled' : '';
  updBanner.querySelector('.acts').innerHTML =
    (m.notes ? '<button class="btn btn-sm" type="button" data-upd="notes">What\'s new</button>' : '') +
    (m.install ? `<button class="btn btn-sm primary" type="button" data-upd="install" ${off}>${esc(m.install)}</button>` : '');
}

async function startInstall(btn) {
  if (btn) btn.disabled = true;
  try {
    const r = await api.installUpdate();
    if (r && r.phase) onUpdate(r);
    if (!updPolling) pollUpdate();
  } catch (e) {
    toast(errText(e), 'err');
    if (btn) btn.disabled = false;
  }
}

// notesHTML renders NoteLine items (internal/update.RenderNotes) as a real
// changelog: version and section headings, bullets grouped into lists,
// paragraphs, gaps.
function notesHTML(notes) {
  if (!notes || !notes.length) return '<p class="m-body">No release notes for this version.</p>';
  let html = '', list = [];
  const flush = () => { if (list.length) { html += `<ul class="nlist">${list.join('')}</ul>`; list = []; } };
  for (const l of notes) {
    if (l.bullet) { list.push(`<li>${esc(l.text)}</li>`); continue; }
    flush();
    if (l.heading) html += `<div class="nl h${/^v\d/.test(l.text) ? ' ver' : ''}">${esc(l.text)}</div>`;
    else if (l.text) html += `<div class="nl">${esc(l.text)}</div>`;
    else html += '<div class="nl gap"></div>';
  }
  flush();
  return html;
}


// updModalContent picks the dialog title/body/buttons for an update snapshot.
function updModalContent(u) {
  const p = u.pending;
  const closeBtn = (t) => `<button class="btn" type="button" data-m="close" data-autofocus>${t}</button>`;
  if (u.devBuild) return { title: 'Dev build - updates disabled', body: '<p class="m-body">This is a development build, so it never checks for or installs updates. Install a release build to get updates.</p>', btns: closeBtn('Close') };
  if (p) {
    const failed = failedPending(u);
    const off = api.offline || updBusy(u) ? 'disabled' : '';
    return {
      title: failed ? `Couldn't install v${p.version}` : `What's new in v${p.version}`,
      body: (failed ? `<p class="m-body m-err">${esc(clean(u.failMessage) || 'The install did not finish.')}</p>` : '') + notesHTML(p.notes),
      btns: `<button class="btn primary" type="button" data-m="install" data-autofocus ${off}>${failed ? 'Retry install' : 'Install now'}</button><button class="btn" type="button" data-m="close">Later</button>`,
    };
  }
  if (u.status === 'checking' || u.phase === 'checking') return { title: 'Checking for updates', body: '<p class="m-body">Dropwarden is checking for a new version.</p>', btns: closeBtn('Close') };
  if (u.status === 'none_published') return { title: 'No releases published yet', body: '<p class="m-body">There is nothing to install yet. Dropwarden checks again automatically.</p>', btns: closeBtn('Close') };
  if (u.status === 'error') return { title: "Couldn't check for updates", body: `<p class="m-body">${esc(clean(u.lastError) || 'The update check failed.')}</p>`, btns: closeBtn('Close') };
  return { title: "You're up to date", body: `<p class="m-body">Dropwarden v${esc(u.currentVersion)} is the latest version.</p>`, btns: closeBtn('Close') };
}

function showUpdateModal(u) {
  if (!u) return;
  const c = updModalContent(u);
  const dlg = el(`<div><h3>${esc(c.title)}</h3><div class="notes">${c.body}</div>${updProgressHTML()}<div class="foot-btns">${c.btns}</div></div>`);
  const close = openModal(dlg);
  dlg.querySelectorAll('[data-m="close"]').forEach(b => b.addEventListener('click', close));
  const ib = dlg.querySelector('[data-m="install"]');
  if (ib) ib.addEventListener('click', () => startInstall(ib));
  renderUpdateProgress(u);
}

// "Updated to vX" shows once after a version change (last seen version in
// localStorage), until dismissed.
let updatedTo = null;
let updatedDismissed = false;

function updatedNotice(state) {
  const ver = state.version || '';
  if (!ver || api.offline) return null;
  if (updatedTo === null) {
    const seen = LS.get('dw.lastSeenVersion');
    LS.set('dw.lastSeenVersion', ver);
    updatedTo = seen && seen !== ver ? ver : '';
  }
  if (!updatedTo || updatedDismissed) return null;
  const notes = (state.update && state.update.installedNotes) || [];
  const b = el(`<div class="banner ok"><span class="ic">${icon('check', 16)}</span>
    <div class="tx"><div class="t">Updated to v${esc(updatedTo)}</div></div>
    <div class="acts">${notes.length ? '<button class="btn btn-sm" type="button" data-n="notes">What\'s new</button>' : ''}<button class="btn btn-sm ghost" type="button" data-n="close">Dismiss</button></div></div>`);
  const nb = b.querySelector('[data-n="notes"]');
  if (nb) nb.addEventListener('click', () => {
    const dlg = el(`<div><h3>What's new in v${esc(updatedTo)}</h3><div class="notes">${notesHTML(notes)}</div><div class="foot-btns"><button class="btn" type="button" data-autofocus>Close</button></div></div>`);
    const close = openModal(dlg);
    dlg.querySelector('button').addEventListener('click', close);
  });
  b.querySelector('[data-n="close"]').addEventListener('click', () => { updatedDismissed = true; b.remove(); });
  return b;
}

// ---- dashboard ----

// loginMsg keeps the "I'm signed in" result per profile/platform so the 5 s
// dashboard rebuild does not wipe it.
const loginMsg = {};

function noteStarted(state) {
  const up = (state.profiles || []).some(p => (p.platforms || []).some(pl => pl.up));
  if (up) LS.set('dw.started', '1');
  return up || LS.get('dw.started') === '1';
}

// firstRunChecklist shows the setup steps until all pass, and comes back when
// one regresses (Chrome gone, signed out, games cleared). Start counts once
// the watcher has ever run, so a plain Stop does not bring it back.
function firstRunChecklist(state) {
  const prof = (state.profiles || []).find(p => p.enabled) || (state.profiles || [])[0];
  if (!prof || api.offline) return null;
  const tw = (prof.platforms || []).find(p => p.platform === 'twitch') || {};
  const steps = [
    { ok: hostInfo.chromeFound !== false, label: 'Chrome found', act: 'chrome', btn: 'Browser settings' },
    hostInfo.remote && tw.up
      ? { ok: !tw.needsLogin, label: 'Sign in to Twitch', act: 'remote', btn: 'Open remote login' }
      : { ok: !tw.needsLogin, label: 'Sign in to Twitch', act: 'login', btn: 'Log in' },
    { ok: !!prof.anyGame || (prof.games || []).length > 0, label: 'Choose your games', act: 'games', btn: 'Choose games' },
    { ok: noteStarted(state), label: 'Start', act: 'start', btn: 'Start' },
  ];
  if (steps.every(s => s.ok)) return null;
  const firstOpen = steps.findIndex(s => !s.ok);
  const items = steps.map((s, i) => `<li class="chk ${s.ok ? 'done' : ''}">
    <span class="chk-ic">${icon(s.ok ? 'check' : 'circle', 14)}</span>
    <span class="chk-l">${i + 1}. ${esc(s.label)}${s.ok ? '<span class="sr"> (done)</span>' : ''}</span>
    ${s.ok ? '' : `<button class="btn btn-sm ${i === firstOpen ? 'primary' : ''}" type="button" data-chk="${s.act}">${esc(s.btn)}</button>`}
  </li>`).join('');
  const box = el(`<div class="card checklist"><h3>Get started</h3><ol>${items}</ol></div>`);
  box.querySelectorAll('[data-chk]').forEach(b => b.addEventListener('click', () => checklistAction(b.dataset.chk, prof.name)));
  return box;
}

async function checklistAction(act, profile) {
  if (act === 'chrome') return openSettings('browser');
  if (act === 'games') { selectedProfile = profile; return openSettings('games'); }
  if (act === 'remote') return openRemoteLogin(profile, 'twitch');
  const a = api.engine('twitch', profile);
  try { await (act === 'login' ? a.login() : a.start()); }
  catch (e) { toast(errText(e), 'err'); }
  refreshAll();
}

// cardKind classifies one platform card: Chrome/launch problems first, then
// sign-in, pause, and the engine state.
function cardKind(p) {
  const st = p.status || {};
  const expired = /session expired/i.test(p.reason || st.reason || '');
  if (!p.up) {
    if (hostInfo.chromeFound === false) return 'chrome';
    if (p.launchError) return 'launch';
    if (p.needsLogin) return 'login-down';
    return p.wanted ? 'launching' : 'stopped';
  }
  if (p.needsLogin || (st.paused && expired)) return 'login';
  if (st.paused) return 'paused';
  const s = st.state || 'idle';
  if (s === 'watching') return 'watching';
  if (s === 'needs_user_action') return 'needs';
  if (s === 'idle' || s === 'cooldown') return 'idle';
  return 'busy';
}

const KIND_PILL = {
  chrome: ['error', 'Needs you'], launch: ['error', 'Needs you'], 'login-down': ['needs', 'Sign in'],
  launching: ['busy', 'Starting'], stopped: ['stopped', 'Stopped'], login: ['needs', 'Needs you'],
  paused: ['paused', 'Paused'], watching: ['watching', 'Watching'], needs: ['needs', 'Needs you'],
};

function kindPill(kind, st) {
  const [cls, text] = KIND_PILL[kind] || [(st.state || 'idle') + (kind === 'busy' ? ' busy' : ''), labelOf(STATE_LABEL, st.state || 'idle')];
  return `<span class="pill ${esc(cls)}"><span class="d"></span> ${esc(text)}</span>`;
}

const BUSY_COPY = {
  discovering: () => 'Checking which of your games have drops right now...',
  selecting_channel: () => 'Finding a live channel for the next drop...',
  opening: (st) => `Starting ${st.channelLogin || 'the stream'}...`,
  validating_playback: (st) => `Making sure ${st.channelLogin || 'the stream'} is playing...`,
  claiming: () => 'Claiming the finished drop...',
  backoff: () => 'Something went wrong. Retrying shortly...',
};

// reasonCopy turns the engine's last transition reason into card copy.
function reasonCopy(reason) {
  const r = String(reason || '').trim();
  if (/nothing eligible/i.test(r)) return { text: 'Nothing to watch right now: none of your games has an active drop with a live channel. Checking again in a few minutes.', games: true };
  if (/no live channel/i.test(r)) return { text: 'No live channel for any of your drops right now. Checking again in a few minutes.', games: true };
  if (/cooldown complete/i.test(r)) return { text: 'Getting ready for the next drop...' };
  if (/minimized/i.test(r)) return { text: 'The Chrome window was minimized. Restoring it so the stream keeps playing.' };
  const claimed = /^claimed (?!\()(.+)/i.exec(r);
  if (claimed) return { text: `Claimed ${claimed[1]}. Getting ready for the next drop...` };
  // Any other engine reason is internal wording: show the plain default.
  return { text: 'Looking for the next drop to watch...' };
}

// QUEUE_REASON puts the engine's queue-row reasons into plain words; a reason
// not listed here shows as it is.
const QUEUE_REASON = [
  [/^user disabled/i, 'Turned off by you'],
  [/not found/i, 'No longer offered'],
  [/window has passed/i, 'Campaign has ended'],
  [/not started yet/i, 'Starts later'],
  [/account link required/i, 'Link your game account to earn this'],
  [/^already claimed|^claimed \(detected/i, 'Already claimed'],
  [/not a watch-time drop/i, 'Earned in the game, not by watching'],
  [/already owned/i, 'You already own this reward'],
  [/claim it .*kick inventory/i, 'Watched. Claim it in your Kick inventory'],
  [/claim it .*twitch inventory/i, 'Watched. Claim it in your Twitch inventory'],
  [/prerequisite/i, 'Earn the earlier drop first'],
  [/^not crediting/i, "Twitch isn't counting progress for this drop"],
  [/channel offline/i, 'The channel went offline'],
  [/different game/i, 'The channel is streaming a different game'],
];
const queueReason = (r) => {
  const s = String(r || '');
  const hit = QUEUE_REASON.find(([re]) => re.test(s));
  return hit ? hit[1] : s;
};

// gamesLink opens Settings > Games; with a profile name, for that profile.
const gamesLink = (profile = '') => ` <button class="linkbtn" type="button" data-choose-games data-profile="${esc(profile)}">Choose games</button>`;

function watchingBody(p, st) {
  const tick = st.current > 0
    ? `<span class="wd">${icon('check', 13)} Progress ticking</span>`
    : `<span class="wd wait">${icon('wait', 13)} Waiting for the first progress update</span>`;
  return `<div class="drop-row">
      <div class="lbl"><span class="name">${esc(st.dropName)}</span><span class="mins mono">${fmtMins(st.current, st.required)}</span></div>
      ${barHTML(st.current, st.required, p.platform === 'kick' ? 'kick' : '', 'Drop progress')}
    </div>
    <div class="watchdogs">${tick}</div>`;
}

function loginBody(p, st, key) {
  const expired = /session expired/i.test(p.reason || st.reason || '');
  const m = loginMsg[key];
  return `<div class="card-msg">
    <p>${expired ? `Your ${esc(platLabel(p.platform))} session expired. ` : ''}${hostInfo.remote ? `Press Open remote login and sign in to ${esc(platLabel(p.platform))} there` : `Sign in to ${esc(platLabel(p.platform))} in the Chrome window Dropwarden opened`}, then press I'm signed in.</p>
    <div class="login-msg ${m ? esc(m.cls) : ''}" aria-live="polite">${m ? esc(m.text) : ''}${m && m.detail ? `<div class="login-detail muted">Details: ${esc(m.detail)}</div>` : ''}</div>
  </div>`;
}

function cardBody(kind, p, st, key) {
  const msg = (t) => `<div class="card-msg"><p>${t}</p></div>`;
  const plat = esc(platLabel(p.platform));
  switch (kind) {
    case 'chrome': return msg('Google Chrome was not found. Install Chrome, then press Start. Installed somewhere else? Set the path in <button class="linkbtn" type="button" data-act="chrome">Settings &gt; Browser</button>.');
    case 'launch': return msg(`Chrome could not start: ${esc(clean(p.launchError))}. Check the path in <button class="linkbtn" type="button" data-act="chrome">Settings &gt; Browser</button>, then press Try again.`);
    case 'login-down': return /session expired/i.test(p.reason || '')
      ? msg(`Your ${plat} session expired. Log in again to keep earning drops.`)
      : msg(hostInfo.remote
        ? `${plat} is not signed in yet. Press Log in, then sign in through Open remote login.`
        : `${plat} is not signed in yet. Press Log in, sign in in the window that opens, then press I'm signed in.`);
    case 'launching': return msg('Starting Chrome...');
    case 'stopped': return msg('Stopped. Press Start to open Chrome and start earning drops.');
    case 'login': return loginBody(p, st, key);
    case 'paused': return msg(hostInfo.remote
      ? 'Paused. Chrome stays open but nothing is being watched. Press Resume to continue.'
      : 'Paused from the tray menu. Chrome stays open but nothing is being watched.');
    case 'watching': return watchingBody(p, st);
    case 'needs': return msg('A drop needs you. <button class="linkbtn" type="button" data-act="review">Review the queue</button> to see what to do.');
    case 'busy': return msg(esc((BUSY_COPY[st.state] || (() => 'Working...'))(st)));
    default: {
      const r = reasonCopy(p.reason || st.reason);
      return msg(esc(r.text) + (r.games ? gamesLink((st && st.profile) || '') : ''));
    }
  }
}

// cardBusy holds the profile/platform keys with an engine request in flight:
// their buttons stay aria-disabled, across re-renders too, until it settles.
const cardBusy = new Set();

function cardButtons(kind, p, st, key) {
  const off = api.offline || api.disconnected ? 'disabled' : (cardBusy.has(key) ? 'aria-disabled="true"' : '');
  const b = (act, text, cls = '') => `<button class="btn ${cls}" type="button" data-act="${act}" ${off}>${text}</button>`;
  const stop = b('stop', 'Stop', 'ghost danger');
  const refresh = `<button class="btn ghost" type="button" data-act="refresh" ${st.paused ? 'disabled title="Resume first"' : off}>Check for drops</button>`;
  switch (kind) {
    case 'chrome': return b('start', 'Start', 'primary') + b('chrome', 'Browser settings', 'ghost');
    case 'launch': return b('start', 'Try again', 'primary') + b('chrome', 'Browser settings', 'ghost');
    case 'login-down': return b('login', 'Log in', 'primary') + b('start', 'Start anyway', 'ghost');
    case 'launching': return stop;
    case 'stopped': return b('start', 'Start', 'primary');
    case 'login': {
      const extra = (loginMsg[key] || {}).unknown ? b('start', 'Start') : '';
      // The headless service has no window on this screen: the mirror is
      // the way in, so it takes the primary slot.
      const remote = hostInfo.remote ? b('remote', 'Open remote login', 'primary') : '';
      const pri = hostInfo.remote ? '' : 'primary';
      if (/session expired/i.test(p.reason || st.reason || '')) return remote + b('login', 'Log in', pri) + b('check-login', "I'm signed in") + extra + stop;
      return remote + b('check-login', "I'm signed in", pri) + extra + stop;
    }
    case 'paused': return b('start', 'Resume', 'primary') + stop;
    case 'watching': return b('skip', 'Skip drop') + refresh + stop;
    case 'needs': return b('review', 'Review', 'primary') + refresh + stop;
    default: return refresh + stop;
  }
}

// nowCardEl renders one platform's card for a profile.
function nowCardEl(p, multi, profile) {
  const st = p.status || {};
  const key = profile + '/' + p.platform;
  const kind = cardKind(p);
  if (kind !== 'login') delete loginMsg[key];
  const showChannel = ['watching', 'busy'].includes(kind) && st.channelLogin;
  const headName = showChannel ? st.channelLogin : platLabel(p.platform);
  const card = el(`<div class="card now" data-fk="c:${esc(key)}">
    <div class="head">
      <div class="avatar" aria-hidden="true">${esc((showChannel ? st.channelLogin : platLabel(p.platform)).slice(0, 2).toUpperCase())}</div>
      <div class="who">
        <div class="ch"><span class="nm">${esc(headName)}</span> ${multi && showChannel ? pfBadge(p.platform) : ''} ${kind === 'watching' ? '<span class="live">LIVE</span>' : ''}</div>
        <div class="game">${esc(st.game || '')}${p.up && !p.enabled ? ' - turned off in Settings, stopping' : ''}</div>
      </div>
      ${kindPill(kind, st)}
    </div>
    ${cardBody(kind, p, st, key)}
    <div class="foot-btns">${cardButtons(kind, p, st, key)}</div>
  </div>`);
  card.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', () => cardAction(b.dataset.act, p, profile, card)));
  return card;
}

// checkLogin runs the sign-in probe; cardAction refreshes the views after.
async function checkLogin(p, profile) {
  const key = profile + '/' + p.platform;
  loginMsg[key] = { text: 'Checking...', cls: '' };
  renderDashboard();
  try {
    const r = await api.loginCheck(profile, p.platform);
    if (r.loggedIn === true) loginMsg[key] = { text: 'Signed in. Starting the watcher...', cls: 'ok' };
    else if (r.loggedIn === false) loginMsg[key] = { text: 'Not signed in yet. Finish signing in, then press I\'m signed in again.', cls: 'warn' };
    else loginMsg[key] = { text: `Dropwarden can't confirm the ${platLabel(p.platform)} sign-in by itself. If you are signed in, press Start.`, cls: '', unknown: true, detail: r.detail || '' };
  } catch (e) {
    loginMsg[key] = { text: 'Check failed: ' + errText(e), cls: 'err' };
  }
}

// ---- remote login (headless service only) ----

// openRemoteLogin mirrors one watcher tab over /api/remote/.../ws: JPEG
// frames are drawn on a canvas, and mouse and keyboard go back as input
// events (x/y as fractions of the frame). Printable keys are sent as text
// (char), so typing works without the remote side knowing the keyboard
// layout. One mirror per window: a second view is refused by the server.
let remoteClose = null;

function openRemoteLogin(profile, platform) {
  if (remoteClose) remoteClose();
  const name = platLabel(platform);
  const back = el(`<div class="modal-back remote-back">
    <div class="remote card" role="dialog" aria-modal="true" aria-label="Remote login: ${esc(name)}">
      <div class="remote-head">
        <h3>Sign in to ${esc(name)}</h3>
        <span class="remote-status muted" aria-live="polite">Connecting...</span>
        <div class="acts"><button class="btn" type="button" data-r="check">Login check</button><button class="btn" type="button" data-r="close">Close</button></div>
      </div>
      <div class="remote-stage"><canvas class="remote-canvas" width="1280" height="720" tabindex="0" aria-label="${esc(name)} browser window. Click it to control the window."></canvas></div>
      <p class="muted remote-hint">Click the picture to control the window and type to enter text. Ctrl+V pastes text from this computer.</p>
    </div>
  </div>`);
  const status = $('.remote-status', back);
  const canvas = $('canvas', back);
  const g = canvas.getContext('2d');
  const setStatus = (t) => { status.textContent = t; };

  const dpr = window.devicePixelRatio || 1;
  const maxW = Math.round(Math.min(3840, Math.max(320, window.innerWidth * dpr)));
  const maxH = Math.round(Math.min(2160, Math.max(240, window.innerHeight * dpr)));
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${scheme}//${location.host}/api/remote/${encodeURIComponent(profile)}/${encodeURIComponent(platform)}/ws?maxWidth=${maxW}&maxHeight=${maxH}`);
  ws.binaryType = 'blob';
  let opened = false, closed = false, frames = 0, meta = null;
  const send = (m) => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m)); };

  // Decode one frame at a time and keep only the newest waiting one, so a
  // slow machine skips frames instead of falling behind.
  let decoding = false, waiting = null;
  const draw = async (blob) => {
    if (decoding) { waiting = blob; return; }
    decoding = true;
    try {
      const bmp = await createImageBitmap(blob);
      if (canvas.width !== bmp.width || canvas.height !== bmp.height) { canvas.width = bmp.width; canvas.height = bmp.height; }
      g.drawImage(bmp, 0, 0);
      bmp.close();
      if (!frames++) setStatus('Connected' + (meta ? ` (${meta.width} x ${meta.height})` : ''));
    } catch (_) { /* skip a frame that failed to decode */ }
    decoding = false;
    if (waiting) { const b = waiting; waiting = null; draw(b); }
  };
  ws.onopen = () => { opened = true; setStatus('Connected. Waiting for the window to draw...'); };
  ws.onmessage = (e) => {
    if (typeof e.data !== 'string') { draw(e.data); return; }
    try { const m = JSON.parse(e.data); if (m.type === 'meta') meta = m; } catch (_) { /* ignore */ }
  };
  ws.onclose = () => {
    if (closed) return;
    setStatus(opened
      ? 'Disconnected: the window closed or restarted. Close this and open it again.'
      : "Couldn't connect: the window may not be running, or it is already open in another remote login view.");
  };

  const pos = (e) => { const r = canvas.getBoundingClientRect(); return { x: (e.clientX - r.left) / r.width, y: (e.clientY - r.top) / r.height }; };
  const mods = (e) => (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
  // Moves are coalesced to one per animation frame and flushed before any
  // press or release, so the remote side never sees them out of order.
  let move = null;
  const flushMove = () => { if (move) { send(move); move = null; } };
  canvas.addEventListener('mousemove', e => {
    const first = !move;
    move = { type: 'mousemove', ...pos(e), buttons: e.buttons, modifiers: mods(e) };
    if (first) requestAnimationFrame(flushMove);
  });
  const press = (type) => (e) => {
    e.preventDefault();
    if (type === 'mousedown') canvas.focus();
    flushMove();
    send({ type, ...pos(e), button: e.button, buttons: e.buttons, modifiers: mods(e) });
  };
  canvas.addEventListener('mousedown', press('mousedown'));
  canvas.addEventListener('mouseup', press('mouseup'));
  canvas.addEventListener('contextmenu', e => e.preventDefault());
  canvas.addEventListener('wheel', e => {
    e.preventDefault();
    const k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? canvas.clientHeight : 1;
    send({ type: 'wheel', ...pos(e), deltaX: e.deltaX * k, deltaY: e.deltaY * k, modifiers: mods(e) });
  }, { passive: false });

  const altGr = (e) => !!(e.getModifierState && e.getModifierState('AltGraph'));
  const printable = (e) => e.key.length === 1 && (altGr(e) || (!e.ctrlKey && !e.metaKey));
  const isPaste = (e) => (e.ctrlKey || e.metaKey) && (e.key === 'v' || e.key === 'V');
  const keyMsg = (type, e) => ({ type, key: e.key, code: e.code, keyCode: e.keyCode, modifiers: mods(e) });
  canvas.addEventListener('keydown', e => {
    if (isPaste(e)) return; // the paste listener sends the clipboard text
    e.preventDefault();
    if (printable(e)) send({ type: 'char', text: e.key });
    else send(keyMsg('keydown', e));
  });
  canvas.addEventListener('keyup', e => {
    if (isPaste(e) || printable(e)) return;
    e.preventDefault();
    send(keyMsg('keyup', e));
  });
  const onPaste = (e) => {
    if (document.activeElement !== canvas) return;
    const t = e.clipboardData && e.clipboardData.getData('text');
    if (t) { e.preventDefault(); send({ type: 'char', text: t }); }
  };
  document.addEventListener('paste', onPaste);
  // Escape belongs to the remote page while the canvas has focus; anywhere
  // else in the view it closes the view.
  const onKey = (e) => { if (e.key === 'Escape' && document.activeElement !== canvas) { e.preventDefault(); close(); } };
  back.addEventListener('keydown', onKey);

  const prev = document.activeElement;
  const close = () => {
    if (closed) return;
    closed = true;
    ws.close();
    document.removeEventListener('paste', onPaste);
    back.remove();
    if (remoteClose === close) remoteClose = null;
    if (prev && prev.isConnected && prev.focus) prev.focus();
    refreshAll();
  };
  $('[data-r="close"]', back).addEventListener('click', close);
  $('[data-r="check"]', back).addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    btn.disabled = true;
    setStatus('Checking the sign-in...');
    try {
      const r = await api.loginCheck(profile, platform);
      if (r.loggedIn === true) { setStatus('Signed in. The watcher starts now; you can close this.'); refreshAll(); }
      else if (r.loggedIn === false) setStatus('Not signed in yet. Finish signing in, then check again.');
      else setStatus(`Dropwarden can't confirm the ${name} sign-in by itself${r.detail ? ' (' + r.detail + ')' : ''}. If you are signed in, close this and press Start.`);
    } catch (err) {
      setStatus('Check failed: ' + errText(err));
    }
    btn.disabled = false;
  });

  document.body.append(back);
  remoteClose = close;
  canvas.focus();
}

const ENGINE_DONE = { skip: 'Skipped to the next drop', refresh: 'Checking for new drops', stop: 'Stopped' };

// cardAction runs one card button. A second press while the first request is
// still running is ignored, and Skip names the drop the card shows so a card
// that is a few seconds old never skips a different drop.
async function cardAction(act, p, profile, card) {
  if (act === 'chrome') return openSettings('browser');
  if (act === 'review') { selectedProfile = profile; return go('queue'); }
  if (act === 'remote') return openRemoteLogin(profile, p.platform);
  const key = profile + '/' + p.platform;
  if (cardBusy.has(key)) return;
  const a = api.engine(p.platform, profile);
  const fn = { 'check-login': () => checkLogin(p, profile), start: a.start, login: a.login, stop: a.stop, skip: () => a.skip((p.status || {}).dropID), refresh: a.refresh }[act];
  if (!fn) return;
  cardBusy.add(key);
  if (card) card.querySelectorAll('.foot-btns [data-act]').forEach(b => b.setAttribute('aria-disabled', 'true'));
  try { await fn(); if (ENGINE_DONE[act]) toast(ENGINE_DONE[act]); }
  catch (e) { toast(errText(e), 'err'); }
  cardBusy.delete(key);
  refreshAll();
}

// needsPhrase names the action a needs-you row asks for.
const ACTION_PHRASE = { 'link account': 'link an account', 'claim on twitch': 'claim a finished drop', 'claim on kick': 'claim a finished drop', 'log in': 'sign in again' };
function needsPhrase(q) {
  const l = String(q.actionLabel || '').trim().toLowerCase();
  if (l) return ACTION_PHRASE[l] || l;
  if (/link/i.test(q.reason || '')) return 'link an account';
  if (/claim/i.test(q.reason || '')) return 'claim a finished drop';
  if (/expired|log ?in/i.test(q.reason || '')) return 'sign in again';
  return 'take a look';
}

// needyBanner counts only rows a human must act on (needsUser) and names the
// actions. profileName is set in the multi-profile per-section form.
function needyBanner(needy, profileName) {
  const phrases = [...new Set(needy.map(needsPhrase))].join(', ');
  const title = `${needy.length === 1 ? '1 drop needs' : needy.length + ' drops need'} you: ${phrases}`;
  const detail = needy.slice(0, 2).map(q => q.dropName).join(', ') + (needy.length > 2 ? ` and ${needy.length - 2} more` : '');
  const banner = el(`<div class="banner needs">
    <span class="ic">${icon('alert', 16)}</span>
    <div class="tx"><div class="t">${esc(title)}</div><div class="d">${esc(detail)}</div></div>
    <div class="acts"><button class="btn btn-sm" type="button">Review</button></div>
  </div>`);
  banner.querySelector('button').addEventListener('click', () => {
    if (profileName) selectedProfile = profileName;
    go('queue');
  });
  return banner;
}

// combineStats sums headline stats across enabled profiles for the shared
// Totals card; successRate is a session-weighted average.
function combineStats(profiles) {
  const s = { dropsEarned: 0, dropsClaimed: 0, minutesWatched: 0, sessions: 0, successRate: 0 };
  let successSum = 0, successWeight = 0;
  for (const p of profiles) {
    const st = p.stats || {};
    s.dropsEarned += st.dropsEarned || 0;
    s.dropsClaimed += st.dropsClaimed || 0;
    s.minutesWatched += st.minutesWatched || 0;
    s.sessions += st.sessions || 0;
    if (st.sessions) { successSum += (st.successRate || 0) * st.sessions; successWeight += st.sessions; }
  }
  s.successRate = successWeight ? Math.round(successSum / successWeight) : 0;
  return s;
}

// upNextHTML lists the next three drops; with several profiles, the next drop
// of each profile instead (unnumbered: there is no order across profiles).
function upNextHTML(profiles, multiProfile) {
  const upnext = multiProfile
    ? profiles.map(p => {
        const q = (p.queue || []).find(x => x.status === 'pending');
        return q && { ...q, profName: p.displayName, profPlatMulti: isMulti(p.platforms) };
      }).filter(Boolean)
    : ((profiles[0] && profiles[0].queue) || []).filter(q => q.status === 'pending').slice(0, 3);
  const soloMulti = !multiProfile && isMulti((profiles[0] || {}).platforms);
  if (!upnext.length) return `<div class="empty">Nothing queued yet.${gamesLink()}</div>`;
  return upnext.map((q, i) => {
    const platBit = (multiProfile ? q.profPlatMulti : soloMulti) ? pfBadge(q.platform) + ' ' : '';
    const profBit = multiProfile ? profBadge(q.profName) + ' ' : '';
    return `<div class="up">${multiProfile ? '' : `<span class="idx">${i + 1}</span>`}<div class="t"><div class="n">${profBit}${platBit}${esc(q.dropName)}</div><div class="g">${esc(q.game)}${q.reason ? ' &middot; ' + esc(queueReason(q.reason)) : ''}</div></div><span class="m mono">${fmtMins(q.currentMinutes, q.requiredMinutes)}</span></div>`;
  }).join('');
}

function totalsHTML(profiles, multiProfile) {
  const soloMulti = !multiProfile && isMulti((profiles[0] || {}).platforms);
  const stats = multiProfile ? combineStats(profiles) : ((profiles[0] && profiles[0].stats) || {});
  const kv = (k, v) => `<div class="kv"><span class="k">${k}</span><span class="v">${v}</span></div>`;
  const rows = multiProfile
    ? profiles.map(p => kv(esc(p.displayName), `${fmtWatch((p.stats || {}).minutesWatched)} &middot; ${(p.stats || {}).dropsClaimed || 0} claimed`)).join('')
    : (soloMulti ? visiblePlatforms((profiles[0] || {}).platforms).map(p => {
        const bp = (((profiles[0] || {}).stats || {}).byPlatform || {})[p.platform] || {};
        return kv(pfBadge(p.platform), `${fmtWatch(bp.minutesWatched)} &middot; ${bp.dropsClaimed || 0} claimed`);
      }).join('') : '');
  return kv('Drops earned', stats.dropsEarned || 0) + kv('Drops claimed', stats.dropsClaimed || 0) +
    kv('Watch time', fmtWatch(stats.minutesWatched)) + kv('Sessions', stats.sessions || 0) +
    kv('Session success', (stats.successRate || 0) + '%') + rows;
}

function dashboardCards(profiles, v) {
  const left = el(`<div class="stack"></div>`);
  if (profiles.length <= 1) {
    const prof = profiles[0] || { name: '', queue: [], platforms: [] };
    const needy = (prof.queue || []).filter(q => q.needsUser);
    if (needy.length) v.append(needyBanner(needy));
    const row = el(`<div class="now-row"></div>`);
    for (const p of visiblePlatforms(prof.platforms)) row.append(nowCardEl(p, isMulti(prof.platforms), prof.name));
    left.append(row);
    return left;
  }
  for (const prof of profiles) {
    const needy = (prof.queue || []).filter(q => q.needsUser);
    const section = el(`<div class="profile-section"><div class="profile-head"><h3 class="pname">${esc(prof.displayName)}</h3></div></div>`);
    if (needy.length) section.append(needyBanner(needy, prof.name));
    const row = el(`<div class="now-row"></div>`);
    for (const p of visiblePlatforms(prof.platforms)) row.append(nowCardEl(p, isMulti(prof.platforms), prof.name));
    section.append(row);
    left.append(section);
  }
  return left;
}

async function renderDashboard() {
  const state = await api.state();
  hostInfo = state.host || {};
  if (state.update) onUpdate(state.update);
  // Visible mirrors the platform rule: enabled, OR still running while a
  // just-disabled profile's engines wind down.
  const profiles = (state.profiles || []).filter(p => p.enabled || (p.platforms || []).some(pl => pl.up));
  const multiProfile = profiles.length > 1;
  const anyMulti = profiles.some(p => isMulti(p.platforms));

  const v = $('#v-dashboard');
  const fk = focusKey(v);
  v.innerHTML = '';
  v.append(el(`<div>
    <h2 class="title">Now watching</h2>
    <p class="sub">${anyMulti ? 'One channel per platform, each in its own background Chrome window.' : 'One channel at a time, in a background Chrome window.'}</p>
  </div>`));
  const done = updatedNotice(state);
  if (done) v.append(done);
  if (updBanner && !updBanner.hidden) v.append(updBanner);
  const chk = firstRunChecklist(state);
  if (chk) v.append(chk);

  const left = dashboardCards(profiles, v);
  const side = el(`<div class="stack">
    <div class="card mini"><h3>Up next</h3><div class="upnext">${upNextHTML(profiles, multiProfile)}</div></div>
    <div class="card mini"><h3>Totals</h3>${totalsHTML(profiles, multiProfile)}</div>
  </div>`);
  const grid = el(`<div class="grid2"></div>`);
  grid.append(left, side);
  v.append(grid);
  restoreFocus(v, fk);
}

// ---- queue ----

const PRIO_LABEL = {
  top_down: 'your order',
  ending_soonest: 'ending soonest',
  closest_to_complete: 'closest to complete',
  shortest_first: 'shortest first',
};

// queueOrderText describes the selection sort keys in order (queue.Ordering
// in internal/queue/select.go).
// queueProf is the profile the Queue view shows: its own games list orders the
// sections and its queue policy is what the header describes and edits.
let queueProf = null;
let queueMulti = false;

function queueOrderText(mode) {
  const chan = !queueProf || queueProf.preferChannelSpecific !== false ? ', streamer-specific drops before any-channel drops' : '';
  const whose = queueMulti ? "this profile's games" : 'your games';
  return `Pinned drops first, then ${whose} in list order${chan}, then ${PRIO_LABEL[mode] || PRIO_LABEL.top_down}`;
}

function setQueueOrderText(mode) {
  const t = document.querySelector('#qorder');
  if (t) t.textContent = queueOrderText(mode);
}

let queuePlatFilter = 'all';
let queueFocusID = null;

const gIndex = (games, g) => (games || []).findIndex(x => String(x).toLowerCase() === String(g || '').toLowerCase());

// queueSections groups the queue: Pinned first (by rank), then one section
// per game in the saved games-list order, unlisted games last.
function queueSections(queue, games) {
  const pinned = queue.filter(q => q.pin > 0).sort((a, b) => a.pin - b.pin);
  const rest = queue.filter(q => !(q.pin > 0)).sort((a, b) => (a.position || 0) - (b.position || 0));
  const byGame = new Map();
  for (const q of rest) {
    if (!byGame.has(q.game)) byGame.set(q.game, []);
    byGame.get(q.game).push(q);
  }
  const names = [...byGame.keys()];
  // Unlisted games keep their first-seen order (read from a copy: sort
  // reorders names while it runs).
  const seen = new Map(names.map((n, i) => [n, i]));
  const rank = (g) => { const i = gIndex(games, g); return i < 0 ? games.length + seen.get(g) : i; };
  names.sort((a, b) => rank(a) - rank(b));
  const secs = pinned.length ? [{ key: '__pinned', title: 'Pinned', rows: pinned, pinned: true }] : [];
  for (const g of names) secs.push({ key: g, title: g || 'Other', rows: byGame.get(g), listed: gIndex(games, g) >= 0 });
  return secs;
}

// reorderIds rebuilds the full queue order after one section changed: the
// section's rows keep the positions they held, filled in their new order.
function reorderIds(queue, sectionIds) {
  const set = new Set(sectionIds);
  const it = sectionIds[Symbol.iterator]();
  return [...queue].sort((a, b) => (a.position || 0) - (b.position || 0))
    .map(q => set.has(q.dropID) ? it.next().value : q.dropID);
}

function queueActionsHTML(q, pinCount) {
  const pin = q.pin > 0 ? q.pin : 0;
  const fw = !!q.forceWatch;
  const name = esc(q.dropName || q.dropID);
  // Owned can be stale on a drop earned in this instance; such a row only
  // needs a manual claim, never a rewatch.
  const earned = q.status === 'claimed' || (q.requiredMinutes > 0 && q.currentMinutes >= q.requiredMinutes);
  const open = q.actionURL ? `<button class="btn btn-sm primary open" type="button">${esc(q.actionLabel || 'Open')}</button>` : '';
  const force = q.owned && !earned
    ? `<button class="btn btn-sm force${fw ? ' on' : ''}" type="button" aria-pressed="${fw}" title="Watch even though you already own it">${fw ? 'Watching anyway' : 'Watch anyway'}</button>`
    : '';
  const ranks = pin
    ? `<button class="qbtn" type="button" data-dir="up" aria-label="Move ${name} up"${pin <= 1 ? ' disabled' : ''}>&#9650;</button>` +
      `<button class="qbtn" type="button" data-dir="down" aria-label="Move ${name} down"${pin >= pinCount ? ' disabled' : ''}>&#9660;</button>`
    : '';
  const starLabel = pin ? `Unpin ${name} (pinned rank ${pin})` : `Pin ${name}: watch it before everything else`;
  const star = `<button class="qbtn star${pin ? ' on' : ''}" type="button" aria-pressed="${!!pin}" aria-label="${starLabel}" title="${starLabel}">${pin ? '&#9733;' + pin : '&#9734;'}</button>`;
  return `<div class="qact">${open}${force}${ranks}${star}</div>`;
}

// liveInfo renders the live-channel dot and meta text for a queue row from
// the engine's last status check (liveLogins: null = never checked, [] =
// none live). The dot sits before the name; the meta lists up to three
// logins so the row still fits on one line.
function liveInfo(q) {
  const logins = q.liveLogins;
  if (!Array.isArray(logins)) return { dot: '', meta: '' };
  const at = q.liveCheckedAt ? new Date(q.liveCheckedAt).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : '';
  if (!logins.length) return { dot: `<span class="ldot" title="No allowed channel was live at ${esc(at)}"></span>`, meta: '' };
  const shown = logins.slice(0, 3).map(esc).join(', ') + (logins.length > 3 ? ` +${logins.length - 3}` : '');
  return {
    dot: `<span class="ldot on" title="Live at ${esc(at)}: ${esc(logins.join(', '))}"></span>`,
    meta: `<span class="lmeta">live: ${shown}</span>`,
  };
}

function queueRowHTML(q, ctx, sec) {
  const active = q.status === 'active';
  const cls = 'qrow' + (active ? ' active' : '') + (q.pin > 0 ? ' pinned' : '');
  const name = esc(q.dropName || q.dropID);
  const badge = q.alongside
    ? '<span class="qbadge" title="A streamer-specific drop of this game also credits this any-channel drop, so it waits behind the streamer drops">also earned by the streamer drops above</span> '
    : '';
  const barCls = active ? (q.platform === 'kick' ? 'kick' : '') : 'blue';
  const mins = fmtMins(q.currentMinutes, q.requiredMinutes);
  const status = esc(labelOf(STATUS_LABEL, q.status));
  const live = liveInfo(q);
  const metaParts = [sec.pinned ? esc(q.game) : '', esc(queueReason(q.reason)), live.meta].filter(Boolean);
  const meta = metaParts.join(' &middot; ');
  return `<div class="${cls}" data-id="${esc(q.dropID)}" data-fk="q:${esc(q.dropID)}" tabindex="0" role="group" aria-label="${name}" draggable="${sec.pinned ? 'false' : 'true'}">
    <span class="grip" title="${sec.pinned ? 'Pinned: use the arrows to change rank' : 'Drag (or Alt+Up/Down) to reorder within this game'}" aria-hidden="true">&#8285;&#8285;</span>
    <div class="qname"><div class="n">${live.dot}${ctx.multi ? pfBadge(q.platform) + ' ' : ''}${name}</div><div class="meta">${badge}${meta}</div></div>
    <div class="qmini">${barHTML(q.currentMinutes, q.requiredMinutes, barCls, 'Progress for ' + (q.dropName || q.dropID))}<div class="mm mono">${mins}</div></div>
    ${queueActionsHTML(q, ctx.pinCount)}
    <div class="qstat"><span class="pill ${esc(q.status)}" title="${status}"><span class="d"></span><span class="t">${status}</span></span></div>
    <div class="switch ${q.enabled ? 'on' : ''}" role="switch" aria-checked="${!!q.enabled}" aria-label="Include ${name} in the queue" tabindex="0"><i></i></div>
  </div>`;
}

function wireForceButton(btn, q, profile) {
  btn.addEventListener('click', async (e) => {
    e.stopPropagation();
    const on = btn.getAttribute('aria-pressed') !== 'true';
    const paint = (v) => {
      btn.setAttribute('aria-pressed', String(v));
      btn.textContent = v ? 'Watching anyway' : 'Watch anyway';
      btn.classList.toggle('on', v);
    };
    paint(on);
    try { await api.setForceWatch(q.dropID, on, profile); }
    catch (err) { paint(!on); toast(errText(err), 'err'); }
  });
}

function wirePinButtons(row, q, profile) {
  row.querySelector('.star').addEventListener('click', async (e) => {
    e.stopPropagation();
    try { await api.setPin(q.dropID, !(q.pin > 0), profile); } catch (err) { toast(errText(err), 'err'); }
    renderQueue();
  });
  row.querySelectorAll('[data-dir]').forEach(b => b.addEventListener('click', async (e) => {
    e.stopPropagation();
    try { await api.setPinRank(q.dropID, b.dataset.dir, profile); } catch (err) { toast(errText(err), 'err'); }
    renderQueue();
  }));
}

function wireEnableSwitch(row, q, profile) {
  const sw = row.querySelector('.switch');
  const flip = async () => {
    const on = !sw.classList.contains('on');
    sw.classList.toggle('on', on);
    sw.setAttribute('aria-checked', String(on));
    try { await api.setEnabled(q.dropID, on, profile); }
    catch (err) { sw.classList.toggle('on', !on); sw.setAttribute('aria-checked', String(!on)); toast(errText(err), 'err'); }
  };
  sw.addEventListener('click', flip);
  sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); flip(); } });
}

function wireOpenButton(row, q, profile) {
  const b = row.querySelector('.open');
  if (!b) return;
  b.addEventListener('click', async (e) => {
    e.stopPropagation();
    // The headless service's Chrome has no screen and the host has no desktop
    // browser: a sign-in goes through the remote login view, any other page
    // opens in this browser.
    if (hostInfo.remote) {
      if (/^log in$/i.test(String(q.actionLabel || '').trim())) return openRemoteLogin(profile, q.platform);
      window.open(q.actionURL, '_blank', 'noopener');
      return;
    }
    try { await api.open(profile, q.platform, q.actionURL); toast('Opened in Chrome'); }
    catch (err) { toast(errText(err), 'err'); }
  });
}

// paintPriority shows mode on the profile, the Queue select and its
// description; setSavedPriority records a mode the server accepted in the
// Settings copies of the profile, so a later Settings Discard or Save cannot
// write the old mode back.
function paintPriority(mode) {
  if (queueProf) queueProf.priorityMode = mode;
  const sel = document.querySelector('#prio');
  if (sel) sel.value = mode;
  setQueueOrderText(mode);
}
function setSavedPriority(mode) {
  if (queueProf) syncProfileField(queueProf.name, 'priorityMode', mode);
}

// ensureTopDown: a manual reorder implies "your order"; switch so it applies,
// and roll back if the server refuses.
async function ensureTopDown() {
  if (!queueProf || queueProf.priorityMode === 'top_down') return;
  const prev = queueProf.priorityMode;
  paintPriority('top_down');
  try {
    await api.setPriority('top_down', queueProf.name);
    setSavedPriority('top_down');
  } catch (err) {
    paintPriority(prev);
    toast(errText(err), 'err');
  }
}

// moveRowKey handles Alt+ArrowUp/Down: a pin rank step in Pinned, a swap
// with the neighbor row inside a game section otherwise.
async function moveRowKey(q, dir, sec, ctx) {
  try {
    if (sec.pinned) await api.setPinRank(q.dropID, dir < 0 ? 'up' : 'down', ctx.profile);
    else {
      const ids = sec.rows.map(r => r.dropID);
      const i = ids.indexOf(q.dropID), j = i + dir;
      if (j < 0 || j >= ids.length) return;
      [ids[i], ids[j]] = [ids[j], ids[i]];
      await api.setOrder(reorderIds(ctx.queue, ids), ctx.profile);
      await ensureTopDown();
    }
  } catch (err) { toast(errText(err), 'err'); }
  queueFocusID = q.dropID;
  renderQueue();
}

function queueRowEl(q, sec, ctx) {
  const row = el(queueRowHTML(q, ctx, sec));
  const forceBtn = row.querySelector('.force');
  if (forceBtn) wireForceButton(forceBtn, q, ctx.profile);
  wirePinButtons(row, q, ctx.profile);
  wireEnableSwitch(row, q, ctx.profile);
  wireOpenButton(row, q, ctx.profile);
  row.addEventListener('keydown', e => {
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return;
    e.preventDefault();
    moveRowKey(q, e.key === 'ArrowUp' ? -1 : 1, sec, ctx);
  });
  return row;
}

// moveGameSection swaps two games in the profile's SAVED games list (fresh
// GET, then POST), leaving any unsaved Settings edits of that list alone.
async function moveGameSection(game, other, ctx) {
  try {
    const pc = ((await api.profileConfigs()) || []).find(x => x.name === ctx.profile);
    if (!pc) return;
    const games = [...(pc.games || [])];
    const a = gIndex(games, game), b = gIndex(games, other);
    if (a < 0 || b < 0) return;
    [games[a], games[b]] = [games[b], games[a]];
    await api.updateProfile({ ...pc, games });
    syncProfileField(pc.name, 'games', games);
  } catch (err) { toast(errText(err), 'err'); }
  renderQueue();
}

function queueSectionEl(sec, i, secs, ctx) {
  const listed = secs.filter(s => s.listed);
  const li = listed.indexOf(sec);
  const prev = li > 0 ? listed[li - 1] : null;
  const next = li >= 0 && li < listed.length - 1 ? listed[li + 1] : null;
  const arrows = sec.listed
    ? `<button class="qbtn" type="button" data-gmv="up" aria-label="Move ${esc(sec.title)} up in your games"${prev ? '' : ' disabled'}>&#9650;</button>` +
      `<button class="qbtn" type="button" data-gmv="down" aria-label="Move ${esc(sec.title)} down in your games"${next ? '' : ' disabled'}>&#9660;</button>`
    : (sec.pinned ? '' : '<span class="qsec-note">not in your games list</span>');
  const box = el(`<section class="qsec" data-fk="s:${esc(sec.key)}"><div class="qsec-head"><h3>${esc(sec.title)}</h3><span class="qsec-n">${sec.rows.length}</span><div class="qsec-mv">${arrows}</div></div><div class="card qlist"></div></section>`);
  const up = box.querySelector('[data-gmv="up"]');
  if (up && prev) up.addEventListener('click', () => moveGameSection(sec.key, prev.key, ctx));
  const down = box.querySelector('[data-gmv="down"]');
  if (down && next) down.addEventListener('click', () => moveGameSection(sec.key, next.key, ctx));
  const list = box.querySelector('.qlist');
  for (const q of sec.rows) list.append(queueRowEl(q, sec, ctx));
  if (!sec.pinned && sec.rows.length > 1) attachQueueDrag(list, ctx);
  return box;
}

function platformChips(onPick) {
  const opts = [['all', 'All'], ['twitch', 'Twitch'], ['kick', 'Kick']];
  const bar = el(`<div class="prof-pills" role="group" aria-label="Platform"></div>`);
  for (const [k, t] of opts) {
    const on = queuePlatFilter === k;
    const b = el(`<button type="button" class="prof-pill ${on ? 'active' : ''}" aria-pressed="${on}">${t}</button>`);
    b.addEventListener('click', () => { queuePlatFilter = k; onPick(); });
    bar.append(b);
  }
  return bar;
}

function queueHeaderEl(multi) {
  const h = el(`<div class="qhead">
    <div><h2 class="title">Queue</h2><p class="sub" style="margin:0"><span id="qorder">${esc(queueOrderText(queueProf && queueProf.priorityMode))}</span>. Drag rows, or press Alt+Up/Down, to reorder within a game${multi ? '. Each platform works its own drops' : ''}.</p></div>
    <div class="qprio">
      <label for="prio">Within a game, order by</label>
      <select class="popup" id="prio">
        <option value="top_down">Your order</option>
        <option value="ending_soonest">Ending soonest</option>
        <option value="closest_to_complete">Closest to complete</option>
        <option value="shortest_first">Shortest first</option>
      </select>
    </div>
  </div>`);
  const sel = $('#prio', h);
  sel.value = (queueProf && queueProf.priorityMode) || 'top_down';
  // A mouse press opens the dropdown: hold the poll re-render until a choice
  // is made (queueBusy), then let it run again even though focus stays here.
  // Closing it without a choice (Escape, leaving it) ends the hold too, and
  // queueBusy caps it at 10 s in case neither event comes.
  sel.addEventListener('mousedown', () => { prioChanged = false; prioOpenedAt = Date.now(); });
  sel.addEventListener('blur', () => { prioChanged = true; });
  sel.addEventListener('keydown', e => { if (e.key === 'Escape') prioChanged = true; });
  sel.addEventListener('change', async e => {
    prioChanged = true;
    try {
      await api.setPriority(e.target.value, queueProf && queueProf.name);
      if (queueProf) queueProf.priorityMode = e.target.value;
      setSavedPriority(e.target.value);
      setQueueOrderText(e.target.value);
    } catch (err) { if (queueProf) e.target.value = queueProf.priorityMode; toast(errText(err), 'err'); }
  });
  return h;
}

async function renderQueue() {
  const state = await api.state();
  const { profiles, profile, multiProfile } = pickProfile(state);
  const prof = profile || { name: '', platforms: [], queue: [] };
  const multi = isMulti(prof.platforms);
  // Only current, workable items belong here; claimed/expired live in History.
  const queue = (prof.queue || []).filter(q => q.status !== 'claimed' && q.status !== 'expired');
  queueProf = prof;
  queueMulti = multiProfile;
  const kickOn = multi || (prof.platforms || []).some(p => p.platform === 'kick' && p.enabled);
  if (!kickOn) queuePlatFilter = 'all';
  const shown = queuePlatFilter === 'all' ? queue : queue.filter(q => q.platform === queuePlatFilter);
  // Sections follow the profile's own games list.
  const games = prof.games || [];
  const ctx = { profile: prof.name, multi, queue, pinCount: queue.filter(q => q.pin > 0).length };
  const v = $('#v-queue');
  const key = JSON.stringify([queue, games, queuePlatFilter, selectedProfile, kickOn, multi,
    profiles.map(p => [p.name, p.displayName]), prof.priorityMode, prof.preferChannelSpecific]);
  if (key === lastQueueKey && !queueFocusID && v.childElementCount) return;
  lastQueueKey = key;
  const fk = focusKey(v);
  v.innerHTML = '';
  v.append(queueHeaderEl(multi));
  if (multiProfile) v.append(profilePillBar(profiles, () => renderQueue()));
  if (kickOn) v.append(platformChips(() => renderQueue()));
  const wrap = el(`<div class="qwrap"></div>`);
  if (!shown.length && queue.length) {
    // Drops are queued, just none on the chosen platform.
    const box = el(`<div class="card"><div class="empty">No ${esc(platLabel(queuePlatFilter))} drops right now. <button class="linkbtn" type="button">Show all</button></div></div>`);
    box.querySelector('button').addEventListener('click', () => { queuePlatFilter = 'all'; renderQueue(); });
    wrap.append(box);
  } else if (!shown.length) wrap.append(el(`<div class="card"><div class="empty">No drops queued yet. Drops for your games appear here after the next check.${gamesLink(prof.name)}</div></div>`));
  const secs = queueSections(shown, games);
  secs.forEach((sec, i) => wrap.append(queueSectionEl(sec, i, secs, ctx)));
  v.append(wrap);
  if (queueFocusID) {
    const r = v.querySelector(`.qrow[data-id="${CSS.escape(queueFocusID)}"]`);
    queueFocusID = null;
    if (r) r.focus();
  }
  restoreFocus(v, fk);
}

let dragRow = null;
let prioChanged = true;
let prioOpenedAt = 0;
// lastQueueKey is everything renderQueue draws from: a poll with nothing new
// skips the rebuild, so a screen reader is not re-told the focused row.
let lastQueueKey = '';

function queueDragAfter(list, y) {
  const rows = [...list.querySelectorAll('.qrow:not(.dragging)')];
  let best = { offset: -Infinity, el: null };
  for (const child of rows) {
    const box = child.getBoundingClientRect();
    const offset = y - box.top - box.height / 2;
    if (offset < 0 && offset > best.offset) best = { offset, el: child };
  }
  return best.el;
}

// attachQueueDrag reorders rows inside ONE game section; a drag never leaves
// its section's list. Only a drop inside the list that changed the order is
// saved: a press-and-release, an Escape or a release elsewhere puts the rows
// back and saves nothing.
function attachQueueDrag(list, ctx) {
  let startIds = [];
  let dropped = false;
  const order = () => [...list.querySelectorAll('.qrow')].map(r => r.dataset.id);
  list.querySelectorAll('.qrow').forEach(row => {
    row.addEventListener('dragstart', e => {
      dragRow = row;
      startIds = order();
      dropped = false;
      row.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      try { e.dataTransfer.setData('text/plain', row.dataset.id || ''); } catch (_) { /* optional */ }
    });
    row.addEventListener('dragend', async () => {
      row.classList.remove('dragging');
      const ids = order();
      const moved = ids.join('\n') !== startIds.join('\n');
      if (!dropped || !moved) {
        if (moved) startIds.forEach(id => { const r = list.querySelector(`.qrow[data-id="${CSS.escape(id)}"]`); if (r) list.appendChild(r); });
        dragRow = null;
        return;
      }
      // dragRow clears only after the order is saved: it also gates the poll
      // re-render, which would otherwise snap the list back mid-save.
      try { await api.setOrder(reorderIds(ctx.queue, ids), ctx.profile); await ensureTopDown(); }
      catch (err) { toast(errText(err), 'err'); lastQueueKey = ''; } // redraw the real order
      dragRow = null;
    });
  });
  list.addEventListener('dragover', e => {
    if (!dragRow) return;
    if (!dragRow.isConnected) { dragRow = null; return; } // re-rendered mid-drag
    if (!list.contains(dragRow)) return; // other section: not a drop target
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const after = queueDragAfter(list, e.clientY);
    if (after == null) list.appendChild(dragRow);
    else list.insertBefore(dragRow, after);
  });
  list.addEventListener('drop', e => {
    if (!dragRow || !list.contains(dragRow)) return;
    e.preventDefault();
    dropped = true;
  });
}

// ---- history ----

async function renderHistory() {
  const state = await api.state();
  const { profiles, profile, multiProfile } = pickProfile(state);
  const prof = profile || { platforms: [], claims: [], stats: {}, spark: [] };
  const multi = isMulti(prof.platforms);
  const claims = prof.claims || [];
  const stats = prof.stats || {};
  const spark = prof.spark || [];
  const v = $('#v-history');
  const fk = focusKey(v);
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">History</h2><p class="sub" title="A session is one continuous watch of one channel toward one drop. Minutes watched is time on stream; drops that credit together can progress faster than it.">Lifetime totals${multi ? ' across every platform' : ''}, including the session in progress.</p></div>`));
  if (multiProfile) v.append(profilePillBar(profiles, () => renderHistory()));
  v.append(el(`<div class="stat-row">
    <div class="card stat"><div class="big ok">${stats.dropsEarned || 0}</div><div class="cap">Drops earned</div></div>
    <div class="card stat"><div class="big ok">${stats.dropsClaimed || 0}</div><div class="cap">Drops claimed</div></div>
    <div class="card stat"><div class="big">${fmtWatch(stats.minutesWatched)}</div><div class="cap">Watch time</div></div>
    <div class="card stat"><div class="big">${stats.successRate || 0}%</div><div class="cap">Session success</div></div>
    <div class="card stat"><div class="big">${stats.sessions || 0}</div><div class="cap">Sessions</div></div>
  </div>`));
  const claimRows = claims.map(c => `
    <div class="clm"><span class="gift">${icon('gift', 16)}</span><div><div class="n">${multi ? pfBadge(c.platform) + ' ' : ''}${esc(c.dropName)}</div><div class="g">${esc(c.game)}${c.source === 'detected' ? ' &middot; already claimed (found on your account)' : ''}</div></div><div class="when">${esc(fmtDate(c.claimedAt))}</div></div>`).join('');
  const claimsCard = el(`<div class="card mini"><h3>Recent claims</h3><div class="clm-list">${claimRows || `<div class="empty">No claims yet.${gamesLink()}</div>`}</div></div>`);
  const outcomes = stats.outcomeCounts || {};
  const outRows = Object.entries(outcomes).map(([k, n]) => `<div class="kv"><span class="k">${esc(labelOf(OUTCOME_LABEL, k))}</span><span class="v">${n}</span></div>`).join('');
  const side = el(`<div class="stack">
    <div class="card mini"><h3>Minutes watched &middot; last 14 days</h3><div class="spark" role="img" aria-label="Watch time per day for the last 14 days, as bars relative to the busiest day">${spark.map(h => `<i style="height:${h}%"></i>`).join('')}</div></div>
    <div class="card mini"><h3>Session outcomes</h3>${outRows || '<div class="empty">No sessions yet.</div>'}</div>
  </div>`);
  const grid = el('<div class="grid2"></div>');
  grid.append(claimsCard, side);
  v.append(grid);
  restoreFocus(v, fk);
}

// ---- settings ----

let settingsPane = 'games';
let lastDebug = null;
let debugProfile = null;
let appVersion = '';
let hostOS = '';
let restartNeeded = false;

// Settings has two groups: the PROFILE panes edit the selected profile's own
// settings (switcher above them when there is more than one profile); the APP
// panes are shared by every profile.
const SETTINGS_GROUPS = [
  { key: 'profile', label: 'Profile' },
  { key: 'app', label: 'App' },
];
const SETTINGS_PANES = [
  { key: 'games', group: 'profile', label: 'Games', icon: 'games', tint: 'var(--ok-dim)', fg: 'var(--pill-ok)' },
  { key: 'platforms', group: 'profile', label: 'Platforms', icon: 'platforms', tint: 'var(--twitch-dim)', fg: 'var(--twitch-fg)' },
  { key: 'player', group: 'app', label: 'Player', icon: 'player', tint: 'var(--accent-dim)', fg: 'var(--pill-accent)' },
  { key: 'browser', group: 'app', label: 'Browser', icon: 'browser', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
  { key: 'app', group: 'app', label: 'App', icon: 'app', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
  { key: 'updates', group: 'app', label: 'Updates', icon: 'up', tint: 'var(--accent-dim)', fg: 'var(--pill-accent)' },
  { key: 'profiles', group: 'app', label: 'Profiles', icon: 'profiles', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
];

// PANE_FIELDS maps each pane to the fields it edits (dirty dots, and which
// pane to open for a field-mapped save error). PROFILE_FIELDS live on the
// selected profile (profileEdits); the rest on cfg.
const PANE_FIELDS = {
  games: ['anyGame', 'games', 'priorityMode', 'autoEnqueueNew', 'fallThrough', 'preferChannelSpecific'],
  platforms: ['kickEnabled', 'skipAccountLink', 'claimButtonSelector', 'claimButtonText'],
  player: ['volume', 'forceLowestQuality', 'muteAudio', 'startMinimized'],
  browser: ['chromePath', 'alwaysOnBottom'],
  app: ['theme', 'keepAwake'],
  updates: ['updateEnabled', 'updateAutoInstall', 'updateShowChangelog', 'updateChannel', 'updateIntervalHours', 'updateFeedURL'],
};
// Fields that only apply once the watcher Chrome restarts.
const RESTART_FIELDS = ['chromePath', 'muteAudio', 'startMinimized'];
// Fields whose change warrants a fresh discovery pass after saving.
const DISCOVERY_FIELDS = ['games', 'anyGame', 'autoEnqueueNew'];

const PROFILE_FIELDS = new Set(['games', 'anyGame', 'priorityMode', 'autoEnqueueNew', 'fallThrough', 'preferChannelSpecific', 'skipAccountLink', 'kickEnabled']);
const isProfileField = (f) => PROFILE_FIELDS.has(f);

// curPE / curPS: the selected profile's edit and its untouched snapshot.
function curPE() { const l = profileEdits || []; return l.find(p => p.name === selectedProfile) || l[0] || null; }
function curPS() { const e = curPE(); return e ? (profileSnapshot || []).find(p => p.name === e.name) || null : null; }

const fieldChanged = (f, base = snap()) => {
  if (isProfileField(f)) {
    const e = curPE(), o = curPS();
    return !!e && !!o && JSON.stringify(e[f]) !== JSON.stringify(o[f]);
  }
  return !!cfg && JSON.stringify(cfg[f]) !== JSON.stringify(base[f]);
};
// Pane dots: a profile pane reflects the selected profile; the Profiles
// (manage) pane reflects display names and on/off of every profile.
const profileRowsDirty = () => (profileEdits || []).some(p => {
  const o = (profileSnapshot || []).find(x => x.name === p.name);
  return !o || o.displayName !== p.displayName || o.enabled !== p.enabled;
});
// A profile pane's dot covers EVERY profile's edits of its fields, not only the
// selected one, so switching the pill never hides a pending change.
const profileFieldChanged = (f) => (profileEdits || []).some(p => {
  const o = (profileSnapshot || []).find(x => x.name === p.name);
  return !o || JSON.stringify(o[f]) !== JSON.stringify(p[f]);
});
const paneDirty = (k) => k === 'profiles' ? profileRowsDirty() : (PANE_FIELDS[k] || []).some(f => isProfileField(f) ? profileFieldChanged(f) : fieldChanged(f));
// cfgDirty: the app-wide fields; isDirty adds every profile's edits, which
// the one Save button saves together.
const cfgDirty = () => !!cfg && !!cfgSnapshot && Object.values(PANE_FIELDS).flat().some(f => !isProfileField(f) && fieldChanged(f));
const isDirty = () => cfgDirty() || profilesDirty();

// A disabled toggle renders off and ignores clicks: the backend cannot honour
// it on this OS, so it must not look enabled.
const toggleHTML = (id, on, label, disabled = false, scope = '') => disabled
  ? `<div class="switch disabled" role="switch" aria-checked="false" aria-disabled="true" aria-label="${esc(label)}" tabindex="-1" data-toggle="${id}"${scope ? ` data-scope="${scope}"` : ''}><i></i></div>`
  : `<div class="switch ${on ? 'on' : ''}" role="switch" aria-checked="${!!on}" aria-label="${esc(label)}" tabindex="0" data-toggle="${id}"${scope ? ` data-scope="${scope}"` : ''}><i></i></div>`;

// row renders one settings row (label, hint, control).
const row = (l, h, ctl, stacked = false) => `<div class="row${stacked ? ' stacked' : ''}"><div class="lab"><div class="l">${l}</div>${h ? `<div class="h">${h}</div>` : ''}</div><div class="ctl">${ctl}</div></div>`;
const toggleRow = (id, l, h, disabled = false) => row(esc(l), h, toggleHTML(id, cfg[id], l, disabled));
// ptoggleRow binds a switch to the selected profile's own settings.
const ptoggleRow = (id, l, h) => row(esc(l), h, toggleHTML(id, (curPE() || {})[id], l, false, 'profile'));
const group = (caption, body, extra = '') => `<div class="group"><div class="caption">${caption}</div><div class="card">${body}</div>${extra}</div>`;
const advanced = (title, body) => `<details class="adv"><summary>${title}</summary><div class="card">${body}</div></details>`;

function openSettings(pane) {
  settingsPane = pane;
  if (isActive('settings')) { syncSettingsNav(); renderSettingsContent(); return; }
  go('settings');
}

async function renderSettings() {
  cfg = await loadCfg();
  takeSnapshot();
  // Unsaved profile edits are kept (not reloaded) when coming back. A failed
  // load leaves profileEdits null: the App panes still render, the profile
  // panes show a retry card (renderSettingsContent).
  try { if (!profilesDirty()) await loadProfileEdits(); } catch (_) { profileEdits = null; }
  const state = await api.state();
  hostInfo = state.host || {};
  appVersion = state.version || (state.update && state.update.currentVersion) || '';
  hostOS = hostInfo.os || '';
  if (state.update) updState = state.update;
  const v = $('#v-settings');
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">Settings</h2><p class="sub">Changes apply when you press Save.</p></div>`));
  const wrap = el(`<div class="set-wrap"><nav class="set-nav" id="set-nav" aria-label="Settings sections"></nav><div id="set-content"></div></div>`);
  v.append(wrap);
  const nav = $('#set-nav', wrap);
  // A container install (hostInfo.updates === false) is updated by replacing
  // its image, so it has no Updates pane at all; its version shows on App.
  const panes = SETTINGS_PANES.filter(n => n.key !== 'updates' || hostInfo.updates !== false);
  if (!panes.some(n => n.key === settingsPane)) settingsPane = 'games';
  nav.innerHTML = SETTINGS_GROUPS.map(g => `<div class="sgrp caption">${g.label}</div>` +
    panes.filter(n => n.group === g.key).map(n => `<button type="button" class="sn" data-pane="${n.key}"><span class="sq" style="background:${n.tint};color:${n.fg}">${icon(n.icon, 13)}</span><span class="snl">${n.label}</span><span class="sdot" aria-hidden="true"></span></button>`).join('')).join('');
  nav.querySelectorAll('[data-pane]').forEach(x => x.addEventListener('click', () => {
    settingsPane = x.dataset.pane;
    syncSettingsNav();
    renderSettingsContent();
  }));
  syncSettingsNav();
  renderSettingsContent();
}

function syncSettingsNav() {
  document.querySelectorAll('#set-nav [data-pane]').forEach(s => {
    const on = s.dataset.pane === settingsPane;
    s.classList.toggle('active', on);
    if (on) s.setAttribute('aria-current', 'page'); else s.removeAttribute('aria-current');
  });
  markDirty();
}

// markDirty enables Save/Discard only when something changed and dots the
// panes holding unsaved edits.
function markDirty() {
  // One Save/Discard covers cfg and every profile's edits.
  const d = isDirty();
  const s = $('#save');
  if (s) s.disabled = !d || api.offline;
  const x = $('#discard');
  if (x) x.disabled = !d;
  document.querySelectorAll('#set-nav [data-pane]').forEach(b => {
    const pd = paneDirty(b.dataset.pane);
    b.classList.toggle('dirty', pd);
    b.title = pd ? 'Unsaved changes' : '';
  });
}

// gamesPaneHTML and the profile half of platformsPaneHTML read the SELECTED
// profile's own settings (curPE), never cfg.
function gamesPaneHTML() {
  return group('Games',
    row('Your games', 'Only drops for these games are watched, on every platform. Games higher in the list are watched first (pinned drops still come first).', '<div id="games-ctl"></div>', true) +
    row('Add games', 'Pick a game with drops right now, or search all Twitch categories.', '<div id="games-picker"></div>', true),
    advanced('Advanced', ptoggleRow('anyGame', 'Any game', 'Queue drops for every game and ignore your list.') +
      '<div class="warnnote">' + icon('alert', 13) + ' Watches every drops-enabled campaign; noticeably more Twitch traffic.</div>')) +
  group('Queue',
    row('Within a game, order by', 'How drops of the same game are picked. Pinned drops and the games list order always come first.',
      '<select class="popup" data-field="priorityMode" data-scope="profile" aria-label="Within a game, order by"><option value="top_down">Your order</option><option value="ending_soonest">Ending soonest</option><option value="closest_to_complete">Closest to complete</option><option value="shortest_first">Shortest first</option></select>') +
    ptoggleRow('autoEnqueueNew', 'Add new drops automatically', 'Newly found drops for your games join the queue.') +
    ptoggleRow('fallThrough', 'Watch the next drop when the top one has no live channel', 'Dropwarden goes back to the top drop as soon as it has a live channel. Off = wait for the top drop.') +
    ptoggleRow('preferChannelSpecific', 'Streamer-specific drops first', 'Within a game, drops tied to particular streamers are watched before any-channel drops, which those streamers credit as well.'));
}

function platformsPaneHTML() {
  const claimFields =
    row('Claim button selector (inventory page)', 'Used only when a normal claim fails: Dropwarden opens the Twitch inventory page and clicks the first element matching this CSS selector whose label matches the text below. Empty = only save the page\'s buttons to the debug folder.',
      `<input class="input mono" type="text" data-field="claimButtonSelector" value="${esc(cfg.claimButtonSelector)}" placeholder="empty = save buttons only, no click" aria-label="Claim button selector" style="width:100%">`, true) +
    row('Claim button text', 'Whole label, not case-sensitive.',
      `<input class="input mono" type="text" data-field="claimButtonText" value="${esc(cfg.claimButtonText)}" placeholder="Claim Now" aria-label="Claim button text" style="width:100%">`, true);
  return group('Twitch',
    row(pfBadge('twitch') + ' Twitch drops', 'Always on.', '<span class="pill active"><span class="d"></span> Always on</span>') +
    ptoggleRow('skipAccountLink', 'Skip drops that need a linked game account', 'Drops Twitch is already crediting are watched anyway. Off = watch everything; claims that fail show up in the queue as Needs you.'),
    advanced('Advanced (troubleshooting)', claimFields)) +
  group('Kick',
    row(pfBadge('kick') + ' Kick drops (experimental)', 'Runs next to Twitch with this profile\'s games and queue. Starts or stops when you press Save.', toggleHTML('kickEnabled', (curPE() || {}).kickEnabled, 'Kick drops (experimental)', false, 'profile')) +
    row('First run', 'Sign in to Kick once in its Chrome window; the session is kept. Kick rewards expire if left unclaimed, so keep an eye on the Needs you banner.', ''),
    `<div class="warnnote">${icon('alert', 13)} Kick support is still new. If a Kick drop stops making progress, copy the diagnostics from the Debugging page and send them in.</div>`);
}

function playerPaneHTML() {
  const vol = Math.max(1, Math.min(100, Math.round((cfg.volume || 0.01) * 100)));
  return group('Player',
    row('Watch volume', 'Kept low but never muted: a muted background tab stops earning drops.',
      `<div class="vol"><input type="range" min="1" max="100" step="1" value="${vol}" data-field="volume" aria-label="Watch volume" aria-valuetext="${vol}%"><span class="mono" id="vol-pct">${vol}%</span></div>`) +
    toggleRow('forceLowestQuality', 'Lowest video quality', 'Saves bandwidth and CPU; does not affect drops.') +
    toggleRow('muteAudio', 'Mute Chrome', 'Silences the watcher Chrome while the player itself stays at the volume above, so drops keep crediting. Needs a Chrome restart.') +
    toggleRow('startMinimized', 'Keep Chrome minimized', 'The window restores briefly while a stream starts (a hidden player will not play), then minimizes again. Needs a Chrome restart.'));
}

function browserPaneHTML() {
  const found = hostInfo.chromeFound === false
    ? '<span class="h-err">Chrome was not found. Install Google Chrome or enter its path.</span>'
    : (hostInfo.chromePath ? `Using: <span class="mono">${esc(hostInfo.chromePath)}</span>` : '');
  return group('Browser',
    row('Chrome path', 'Blank = find Chrome automatically. Set it for Chrome Beta or an unusual install. ' + found,
      `<input class="input mono" data-field="chromePath" value="${esc(cfg.chromePath)}" placeholder="found automatically" aria-label="Chrome path" style="width:100%">`, true) +
    toggleRow('alwaysOnBottom', 'Keep Chrome behind other windows (Windows only)', 'Keeps the watcher windows under your other windows without focusing them, so they keep earning drops. Not available on macOS or Linux; cover the window instead.', hostOS !== 'windows'));
}

function appPaneHTML() {
  const version = hostInfo.updates === false
    ? group('Version', row('Current version', 'This install is updated by replacing its container image.', `<span class="mono">v${esc(appVersion)}</span>`))
    : '';
  return version + group('Appearance',
    row('Theme', 'System follows your OS light or dark setting.', '<select class="popup" data-field="theme" aria-label="Theme"><option value="dark">Dark</option><option value="light">Light</option><option value="system">System</option></select>')) +
  group('Power',
    row('Keep the computer awake', 'Stops the PC from idle-sleeping. Monitors still turn off on your normal schedule. With nothing to watch the PC sleeps as usual; nothing here wakes it back up.', '<select class="popup" data-field="keepAwake" aria-label="Keep the computer awake"><option value="while_watching">While watching a drop</option><option value="always">Always, while Dropwarden runs</option><option value="never">Never</option></select>'));
}

function updStatusHTML(u) {
  if (!u) return '';
  const off = api.offline ? 'disabled' : '';
  const p = u.pending;
  const notesBtn = hasNotes(u) ? '<button class="btn" type="button" data-upd="notes">What\'s new</button>' : '';
  const details = (t) => t ? `<details class="upd-details"><summary>Details</summary><pre class="log-pre">${esc(clean(t))}</pre></details>` : '';
  if (u.devBuild) return '<div class="upd-line">Dev build - updates disabled.</div>';
  if (updBusy(u)) return '';
  if (failedPending(u)) return `<div class="upd-line err">Couldn't install v${esc(p.version)}. ${esc(clean(u.failMessage))}</div>${details(u.lastError)}<div class="upd-acts"><button class="btn primary" type="button" data-upd="install" ${off}>Retry install</button>${notesBtn}</div>`;
  if (p && u.deferredReason) return `<div class="upd-line">v${esc(p.version)} is ready. It installs automatically when the current watch ends (${esc(u.deferredReason)}).</div><div class="upd-acts"><button class="btn primary" type="button" data-upd="install" ${off}>Install now</button>${notesBtn}</div>`;
  if (p) return `<div class="upd-line">v${esc(p.version)} is available.</div><div class="upd-acts"><button class="btn primary" type="button" data-upd="install" ${off}>Install now</button>${notesBtn}</div>`;
  if (u.status === 'checking') return '<div class="upd-line">Checking for updates...</div>';
  if (u.status === 'none_published') return '<div class="upd-line">No releases published yet.</div>';
  if (u.status === 'error') return `<div class="upd-line err">Couldn't check for updates.</div>${details(u.lastError)}`;
  if (u.status === 'up_to_date') return `<div class="upd-line ok">You're up to date.${u.lastCheck ? ' Last checked ' + esc(fmtDate(u.lastCheck)) + '.' : ''}</div>`;
  return '';
}

// refreshUpdStatus repaints the Updates pane status block only when its copy
// changed, so an open Details toggle or focused button survives polls.
function refreshUpdStatus(u) {
  const host = $('#upd-status');
  if (!host) return;
  const h = updStatusHTML(u);
  if (host.dataset.k === h) return;
  host.dataset.k = h;
  host.innerHTML = h;
}

function updatesPaneHTML() {
  const u = updState || {};
  const dev = !!u.devBuild;
  const dis = dev || api.offline ? 'disabled' : '';
  return group('Version',
    row('Current version', dev ? 'Dev build - updates disabled.' : '', `<span class="mono">v${esc(appVersion)}</span>`) +
    `<div class="row stacked"><div id="upd-status" aria-live="polite"></div>${updProgressHTML()}<div><button class="btn" id="upd-check" type="button" ${dis}>Check for updates</button></div></div>`) +
  group('Automatic updates',
    row('Release channel', 'Stable follows tagged releases. Dev follows every build of the dev branch: newest features first, rough edges included. Switching back to Stable keeps the dev build until the next release.',
      `<select class="popup" data-field="updateChannel" aria-label="Release channel" ${dis}><option value="stable">Stable</option><option value="dev">Dev</option></select>`) +
    toggleRow('updateEnabled', 'Check automatically', 'Shortly after start, then on the interval below.', dev) +
    toggleRow('updateAutoInstall', 'Install automatically', 'Waits until the current watch ends, installs, and restarts Dropwarden.', dev) +
    toggleRow('updateShowChangelog', "Show what's new", 'Offer the release notes when an update is found.', dev),
    advanced('Advanced',
      row('Check interval', 'Hours between automatic checks.', `<input class="input" type="number" min="1" step="1" value="${esc(cfg.updateIntervalHours)}" data-field="updateIntervalHours" style="width:96px" aria-label="Check interval in hours" ${dis}>`) +
      row('Feed URL', 'Where release information is read from; the channel picks the default. Empty turns update checks off.', `<input class="input mono" type="text" data-field="updateFeedURL" value="${esc(cfg.updateFeedURL)}" placeholder="empty = no update checks" style="width:100%" aria-label="Feed URL" ${dis}>`, true)));
}

function settingsPaneHTML() {
  switch (settingsPane) {
    case 'platforms': return platformsPaneHTML();
    case 'player': return playerPaneHTML();
    case 'browser': return browserPaneHTML();
    case 'app': return appPaneHTML();
    case 'updates': return updatesPaneHTML();
    default: return gamesPaneHTML();
  }
}

const restartBannerHTML = () => restartNeeded
  ? `<div class="banner needs" id="restart-banner"><span class="ic">${icon('info', 16)}</span><div class="tx"><div class="t">Restart Chrome to apply</div><div class="d">Some of the changes you saved take effect when the watcher Chrome restarts.</div></div><div class="acts"><button class="btn btn-sm primary" type="button" data-restart>Restart now</button></div></div>`
  : '';

function renderSettingsContent() {
  const host = $('#set-content');
  if (!host) return;
  const pane = SETTINGS_PANES.find(n => n.key === settingsPane) || SETTINGS_PANES[0];
  const list = profileEdits || [];
  if ((pane.group === 'profile' || settingsPane === 'profiles') && !list.length) {
    host.innerHTML = restartBannerHTML() + `<div class="card"><div class="empty">Couldn't load your profiles right now. <button class="linkbtn" type="button" data-retry>Try again</button></div></div>`;
    host.querySelector('[data-retry]').addEventListener('click', async () => {
      try { await loadProfileEdits(); } catch (_) { /* the card stays */ }
      renderSettingsContent();
    });
    markDirty();
    return;
  }
  if (!list.some(p => p.name === selectedProfile)) selectedProfile = list[0].name;
  const switcher = pane.group === 'profile' && list.length > 1 ? '<div class="caption">Settings for</div><div id="set-prof"></div>' : '';
  host.innerHTML = restartBannerHTML() + switcher +
    (settingsPane === 'profiles' ? '<div id="prof-host"><div class="empty">Loading profiles...</div></div>' : settingsPaneHTML()) +
    `<div class="foot-btns"><button class="btn primary" id="save" type="button">Save</button><button class="btn" id="discard" type="button">Discard changes</button></div>`;
  const sp = $('#set-prof', host);
  if (sp) sp.replaceWith(profilePillBar(list.map(p => ({ name: p.name, displayName: p.displayName || p.name })), () => renderSettingsContent()));
  if (settingsPane === 'profiles') {
    // The manage pane needs each profile's live status; its rows are built
    // once that is in.
    loadProfileLive().then(() => { if (settingsPane === 'profiles') renderProfilesPane($('#prof-host', host)); }).catch(() => {
      const h = $('#prof-host', host);
      if (!h || settingsPane !== 'profiles') return;
      h.innerHTML = `<div class="card"><div class="empty">Couldn't load your profiles right now. <button class="linkbtn" type="button" data-retry>Try again</button></div></div>`;
      h.querySelector('[data-retry]').addEventListener('click', () => renderSettingsContent());
    });
  }
  wireSettingsToggles(host);
  wireSettingsFields(host);
  const themeSel = host.querySelector('select[data-field="theme"]');
  if (themeSel) themeSel.value = cfg.theme || 'dark';
  const awakeSel = host.querySelector('select[data-field="keepAwake"]');
  if (awakeSel) awakeSel.value = cfg.keepAwake || 'while_watching';
  const prioSel = host.querySelector('select[data-field="priorityMode"]');
  if (prioSel) prioSel.value = (curPE() || {}).priorityMode || 'top_down';
  const chanSel = host.querySelector('select[data-field="updateChannel"]');
  if (chanSel) chanSel.value = cfg.updateChannel || 'stable';
  if (settingsPane === 'games') { renderGamesCtl(); mountSharedPicker(); }
  if (settingsPane === 'updates') wireUpdatesPane(host);
  const rb = host.querySelector('[data-restart]');
  if (rb) rb.addEventListener('click', () => restartChrome(rb));
  $('#save', host).addEventListener('click', saveSettings);
  $('#discard', host).addEventListener('click', () => { discardSettings(); renderSettingsContent(); });
  markDirty();
}

function wireSettingsToggles(host) {
  host.querySelectorAll('[data-toggle]').forEach(sw => {
    const flip = () => {
      if (sw.classList.contains('disabled')) return;
      const on = !sw.classList.contains('on');
      sw.classList.toggle('on', on);
      sw.setAttribute('aria-checked', String(on));
      (sw.dataset.scope === 'profile' ? (curPE() || {}) : cfg)[sw.dataset.toggle] = on;
      clearFieldError(sw);
      if (sw.dataset.toggle === 'anyGame') renderGamesCtl();
      markDirty();
    };
    sw.addEventListener('click', flip);
    sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); } });
  });
}

function onFieldInput(inp, host) {
  const f = inp.dataset.field;
  if (inp.dataset.scope === 'profile') {
    const pe = curPE();
    if (pe) pe[f] = inp.value;
    clearFieldError(inp);
    markDirty();
    return;
  }
  if (f === 'volume') {
    const n = parseInt(inp.value, 10);
    if (Number.isFinite(n)) cfg.volume = n / 100;
    const p = $('#vol-pct', host);
    if (p) p.textContent = n + '%';
    inp.setAttribute('aria-valuetext', n + '%');
  } else if (f === 'updateIntervalHours') {
    // An empty or invalid entry is kept as invalid (NaN), so Save can say so
    // instead of quietly saving the old number the field no longer shows.
    const n = parseInt(inp.value, 10);
    cfg.updateIntervalHours = Number.isFinite(n) && n >= 1 ? n : NaN;
  } else if (f === 'theme') {
    cfg.theme = inp.value;
    applyTheme(cfg.theme); // live preview; Discard reverts it
  } else {
    cfg[f] = inp.value;
  }
  clearFieldError(inp);
  markDirty();
}

function wireSettingsFields(host) {
  host.querySelectorAll('[data-field]').forEach(inp => {
    inp.addEventListener(inp.tagName === 'SELECT' ? 'change' : 'input', () => onFieldInput(inp, host));
  });
}

function wireUpdatesPane(host) {
  // A container install (hostInfo.updates === false) renders the pane without
  // the status box or the check button: nothing to wire.
  const status = $('#upd-status', host);
  if (!status) return;
  refreshUpdStatus(updState);
  renderUpdateProgress(updState || {});
  status.addEventListener('click', e => {
    const b = e.target.closest('[data-upd]');
    if (!b) return;
    if (b.dataset.upd === 'notes') showUpdateModal(updState);
    else startInstall(b);
  });
  const btn = $('#upd-check', host);
  btn.addEventListener('click', () => checkUpdatesClick(btn));
}

async function checkUpdatesClick(btn) {
  btn.disabled = true;
  btn.textContent = 'Checking...';
  try {
    const u = await api.checkUpdate();
    // A manual check that finds an update bumps promptSeq, so onUpdate opens
    // the dialog; open it here only when that did not happen (no modal up).
    onUpdate(u);
    if (u.status === 'available' && u.pending && !modalClose) showUpdateModal(u);
  } catch (e) {
    toast(errText(e), 'err');
  }
  btn.disabled = !!(updState && updState.devBuild) || api.offline;
  btn.textContent = 'Check for updates';
}

function clearFieldError(ctl) {
  ctl.classList.remove('invalid');
  ctl.removeAttribute('aria-invalid');
  const r = ctl.closest('.row');
  const e = r && r.querySelector('.field-err');
  if (e) e.remove();
}

// showFieldError opens the pane holding field and marks its control; false
// when the field is not on any pane (the toast still shows the message).
function showFieldError(field, msg) {
  const pane = Object.keys(PANE_FIELDS).find(k => PANE_FIELDS[k].includes(field));
  if (!pane) return false;
  if (pane !== settingsPane) { settingsPane = pane; syncSettingsNav(); renderSettingsContent(); }
  const ctl = document.querySelector(`#set-content [data-field="${field}"], #set-content [data-toggle="${field}"]`) ||
    (field === 'games' ? document.querySelector('#games-ctl') : null);
  if (!ctl) return false;
  const d = ctl.closest('details');
  if (d) d.open = true;
  const r = ctl.closest('.row');
  ctl.classList.add('invalid');
  ctl.setAttribute('aria-invalid', 'true');
  let e = r.querySelector('.field-err');
  if (!e) { e = el('<div class="field-err" role="alert"></div>'); r.querySelector('.lab').append(e); }
  e.textContent = msg;
  if (ctl.focus) ctl.focus();
  return true;
}

function discardSettings() {
  cfg = JSON.parse(cfgSnapshot || '{}');
  resetProfileEdits();
  applyTheme(cfg.theme);
}

const anyEngineUp = (profiles) => (profiles || []).some(p => (p.platforms || []).some(pl => pl.up));

// saveSettings saves cfg (the app-wide fields) and every profile with edits;
// returns true on success. A field-mapped error jumps to and marks that field.
// Each part that saved is snapshotted at once, so a failure further down
// leaves only the unsaved part dirty.
async function saveSettings() {
  const before = snap();
  const restart = RESTART_FIELDS.some(f => fieldChanged(f, before));
  const orig = (p) => (profileSnapshot || []).find(x => x.name === p.name);
  const changed = (profileEdits || []).filter(p => { const o = orig(p); return !o || JSON.stringify(o) !== JSON.stringify(p); });
  const rediscover = changed.some(p => { const o = orig(p) || {}; return DISCOVERY_FIELDS.some(f => JSON.stringify(o[f]) !== JSON.stringify(p[f])); });
  if (cfg.updateIntervalHours !== undefined && !(cfg.updateIntervalHours >= 1)) {
    const m = 'Enter how many hours to wait between update checks (1 or more).';
    showFieldError('updateIntervalHours', m);
    toast(m, 'err');
    return false;
  }
  try {
    if (cfgDirty()) {
      await api.saveConfig(cfg);
      takeSnapshot();
    }
    for (const p of changed) {
      const o = orig(p);
      if (o && o.enabled !== p.enabled) await api.setProfileEnabled(p.name, p.enabled);
      const { enabled: _e, ...rest } = p;
      const { enabled: _o, ...orest } = o || {};
      if (!o || JSON.stringify(rest) !== JSON.stringify(orest)) await api.updateProfile(p);
      const i = (profileSnapshot || []).findIndex(x => x.name === p.name);
      const copy = cloneProfiles([p])[0];
      if (i >= 0) profileSnapshot[i] = copy; else (profileSnapshot = profileSnapshot || []).push(copy);
    }
  } catch (e) {
    const m = errText(e);
    if (e.field) showFieldError(e.field, m);
    toast(m, 'err');
    markDirty();
    return false;
  }
  applyTheme(cfg.theme, true);
  if (restart && anyEngineUp(await api.profiles())) restartNeeded = true;
  if (isActive('settings')) renderSettingsContent();
  toast(rediscover ? 'Saved. Checking for drops for your games...' : 'Saved');
  if (rediscover) refreshDiscovery();
  setTimeout(() => refreshAll(), 500);
  return true;
}

// restartChrome stops then starts every running engine so Chrome picks up
// launch settings, and puts each one back the way it was: a window waiting for
// sign-in comes back on the sign-in page (Start would mark it signed in), and
// a paused one is paused again once it is up (Start would resume it).
async function restartChrome(btn) {
  btn.disabled = true;
  btn.textContent = 'Restarting...';
  const up = [];
  for (const prof of await api.profiles()) {
    for (const p of prof.platforms || []) {
      if (p.up) up.push({ n: prof.name, pl: p.platform, mode: p.needsLogin ? 'login' : (p.status && p.status.paused ? 'paused' : 'watch') });
    }
  }
  try {
    for (const e of up) await api.engine(e.pl, e.n).stop();
    await waitEngines(up, false);
    for (const e of up) {
      const a = api.engine(e.pl, e.n);
      await (e.mode === 'login' ? a.login() : a.start());
    }
    // Pause each formerly paused engine the moment IT is up (a fresh engine
    // starts watching at once), and stop any that never comes back rather
    // than leave it set to watch.
    const left = new Set(up.filter(e => e.mode === 'paused'));
    // A pause that fails must not leave that engine watching, nor skip the
    // engines after it: it joins the stop list below.
    const unpaused = [];
    for (let i = 0; i < 120 && left.size; i++) {
      const ps = (await api.refreshState()).profiles || [];
      for (const e of [...left]) {
        const pr = ps.find(p => p.name === e.n);
        if (pr && (pr.platforms || []).some(x => x.platform === e.pl && x.up)) {
          left.delete(e);
          try { await api.engine(e.pl, e.n).pause(); } catch (err) { unpaused.push(e); }
        }
      }
      if (left.size) await sleep(250);
    }
    for (const e of [...left, ...unpaused]) {
      try { await api.engine(e.pl, e.n).stop(); } catch (err) { /* reported by the toast below */ }
    }
    restartNeeded = false;
    toast(left.size || unpaused.length ? 'Chrome restarted. A paused window did not come back, so it was stopped; press Start when you want it.' : 'Chrome restarted');
  } catch (e) {
    toast(errText(e), 'err');
  }
  if (isActive('settings') && settingsPane !== 'profiles') renderSettingsContent();
  refreshAll();
}

// waitEngines polls until every listed engine is up (wantUp) or down, for at
// most 10 s.
async function waitEngines(list, wantUp) {
  for (let i = 0; i < 20; i++) {
    const profiles = await api.refreshState().then(s => s.profiles || []);
    const pending = list.some(({ n, pl }) => {
      const prof = profiles.find(p => p.name === n);
      const isUp = !!prof && (prof.platforms || []).some(x => x.platform === pl && x.up);
      return isUp !== wantUp;
    });
    if (!pending) return;
    await sleep(500);
  }
}

// confirmLeave asks what to do with unsaved settings: 'save', 'discard' or
// 'cancel' (also Escape/backdrop).
function confirmLeave() {
  return new Promise(resolve => {
    let done = false;
    const finish = (v) => { if (!done) { done = true; resolve(v); } };
    const dlg = el(`<div><h3>Save your changes?</h3><p class="m-body">Some settings have not been saved yet.</p><div class="foot-btns"><button class="btn primary" type="button" data-c="save" data-autofocus>Save</button><button class="btn" type="button" data-c="discard">Discard</button><button class="btn ghost" type="button" data-c="cancel">Cancel</button></div></div>`);
    const close = openModal(dlg, () => finish('cancel'));
    dlg.querySelectorAll('[data-c]').forEach(b => b.addEventListener('click', () => { finish(b.dataset.c); close(); }));
  });
}

// ---- Settings > Games list ----

function gameRowHTML(g, i, n) {
  const name = esc(g);
  return `<div class="grow" data-fk="g:${name}"><span class="grank mono">${i + 1}</span><span class="gname">${name}</span>` +
    `<button class="qbtn" type="button" data-mv="${i}" data-step="-1" aria-label="Move ${name} up"${i === 0 ? ' disabled' : ''}>&#9650;</button>` +
    `<button class="qbtn" type="button" data-mv="${i}" data-step="1" aria-label="Move ${name} down"${i === n - 1 ? ' disabled' : ''}>&#9660;</button>` +
    `<button class="qbtn" type="button" data-rm="${i}" aria-label="Remove ${name}">&times;</button></div>`;
}

// peGames is the selected profile's games list (the Games pane edits it in
// place).
function peGames() {
  const pe = curPE();
  if (!pe) return [];
  if (!pe.games) pe.games = [];
  return pe.games;
}

function moveGame(i, step) {
  const g = peGames();
  const j = i + step;
  if (j < 0 || j >= g.length) return;
  [g[i], g[j]] = [g[j], g[i]];
  renderGamesCtl();
}

function addGame(name) {
  const val = String(name || '').trim();
  if (val && gIndex(peGames(), val) < 0) peGames().push(val);
  renderGamesCtl();
  refreshPickers();
}

function renderGamesCtl() {
  const gamesCtl = document.querySelector('#games-ctl');
  if (!gamesCtl) return;
  const disabled = (curPE() || {}).anyGame;
  gamesCtl.classList.toggle('dim', !!disabled);
  // inert: a list shown as off must not be reachable by keyboard either.
  gamesCtl.inert = !!disabled;
  const games = peGames();
  const rows = games.map((g, i) => gameRowHTML(g, i, games.length)).join('');
  const fk = focusKey(gamesCtl);
  gamesCtl.innerHTML = (disabled ? '<div class="h">Any game is on (Advanced), so this list is ignored.</div>' : '') +
    `<div class="glist">${rows || '<span class="muted">No games yet. Add some below.</span>'}</div>`;
  restoreFocus(gamesCtl, fk);
  gamesCtl.querySelectorAll('[data-rm]').forEach(x => x.addEventListener('click', () => {
    const i = +x.dataset.rm;
    const kb = x === document.activeElement;
    peGames().splice(i, 1);
    renderGamesCtl();
    refreshPickers();
    // The removed row is gone: keyboard focus moves to the next remove button.
    const rest = gamesCtl.querySelectorAll('[data-rm]');
    if (kb && rest.length) rest[Math.min(i, rest.length - 1)].focus();
  }));
  gamesCtl.querySelectorAll('[data-mv]').forEach(b => b.addEventListener('click', () => moveGame(+b.dataset.mv, +b.dataset.step)));
  markDirty();
}

// ---- game picker (Settings > Games) ----

// catalogCache is GET /api/games/catalog, fetched only when a picker opens
// or its Refresh link is pressed (the server caches it too), never on a timer.
let catalogCache = null;
let catalogLoading = null;
let catalogProfile = '';

function loadCatalog(refresh = false) {
  // The catalog is a profile's cached campaigns: switching profiles drops it.
  const prof = (curPE() || {}).name || '';
  if (prof !== catalogProfile) { catalogProfile = prof; catalogCache = null; catalogLoading = null; }
  if (catalogCache && !refresh) return Promise.resolve(catalogCache);
  if (catalogLoading && !refresh) return catalogLoading;
  // A failure keeps the last good list, or stores a failed marker so the
  // picker shows "Could not load the list" instead of loading forever.
  catalogLoading = api.gamesCatalog(refresh, prof)
    .then(c => { catalogCache = c || { games: [] }; return catalogCache; })
    .catch(() => { toast("Couldn't load the games with drops right now. Try Refresh in a moment.", 'err'); catalogCache = catalogCache || { games: [], failed: true }; return catalogCache; })
    .finally(() => { catalogLoading = null; });
  return catalogLoading;
}

const pickerRowHTML = (name, added, meta, img = '') =>
  `<div class="pk-row">${img}<div class="pk-name"><div class="n">${esc(name)}</div>${meta ? `<div class="pk-meta">${meta}</div>` : ''}</div>` +
  `<button class="btn btn-sm ${added ? '' : 'primary'}" type="button" data-pk-add="${esc(name)}" ${added ? 'disabled' : ''} aria-label="${added ? esc(name) + ' added' : 'Add ' + esc(name)}">${added ? 'Added' : 'Add'}</button></div>`;

function catalogMeta(g) {
  const bits = (g.platforms || []).map(pfBadge);
  if (g.campaigns) bits.push(`${g.campaigns} campaign${g.campaigns === 1 ? '' : 's'}`);
  if (g.live) bits.push(`${g.live} live`);
  if (g.endsAt) bits.push('ends ' + esc(fmtDate(g.endsAt)));
  return bits.join(' &middot; ');
}

// renderCatalogList paints "Drops right now", filtered by the search text.
function renderCatalogList(pk) {
  const list = pk.el.querySelector('[data-cat]');
  const meta = pk.el.querySelector('[data-cat-time]');
  const c = catalogCache;
  if (!c) { list.innerHTML = '<div class="muted pk-empty">Loading...</div>'; return; }
  meta.textContent = c.fetchedAt ? 'Updated ' + fmtDate(c.fetchedAt) : '';
  const q = pk.el.querySelector('.pk-search').value.trim().toLowerCase();
  const games = (c.games || []).filter(g => !q || String(g.game).toLowerCase().includes(q));
  if (!games.length) {
    list.innerHTML = `<div class="muted pk-empty">${q ? 'No games with drops match.' : (c.failed ? 'Could not load the list. Try Refresh.' : 'No drops found right now.')}</div>`;
    return;
  }
  list.innerHTML = games.map(g => pickerRowHTML(g.game, pk.has(g.game), catalogMeta(g))).join('');
}

function renderSearchList(pk, results, q) {
  const sec = pk.el.querySelector('[data-search-sec]');
  const list = pk.el.querySelector('[data-res]');
  sec.hidden = !q;
  if (!q) return;
  if (results === null) { list.innerHTML = '<div class="muted pk-empty">Searching...</div>'; return; }
  if (results === false) { list.innerHTML = '<div class="muted pk-empty">Couldn\'t search Twitch right now. Try again in a moment.</div>'; return; }
  if (!results.length) { list.innerHTML = '<div class="muted pk-empty">No Twitch categories match.</div>'; return; }
  list.innerHTML = results.map(r => pickerRowHTML(r.game, pk.has(r.game), '',
    r.boxArt ? `<img class="pk-art" src="${esc(r.boxArt)}" alt="" loading="lazy" width="24" height="32">` : '<span class="pk-art"></span>')).join('');
}

// runSearch debounces typing (400 ms, at least 2 characters) into
// GET /api/games/search; stale responses are dropped.
function onPickerInput(pk) {
  renderCatalogList(pk);
  clearTimeout(pk.timer);
  // Every keystroke makes an answer still in flight stale, not only the
  // next timer firing.
  const seq = ++pk.seq;
  const q = pk.el.querySelector('.pk-search').value.trim();
  pk.lastQ = q.length >= 2 ? q : '';
  if (!pk.lastQ) { pk.results = []; renderSearchList(pk, [], ''); return; }
  renderSearchList(pk, null, q);
  pk.timer = setTimeout(async () => {
    let res;
    try { res = (await api.searchGames(q)) || []; } catch (e) { res = false; }
    if (seq !== pk.seq) return;
    pk.results = res;
    renderSearchList(pk, pk.results, pk.lastQ);
  }, 400);
}

function pickerAdd(pk, name) {
  const val = String(name || '').trim();
  if (!val || pk.has(val)) return;
  pk.add(val);
  pk.refresh();
}

// gamePicker builds one picker: has(game) says whether it is already in the
// list, add(game) adds it. The returned object's refresh() repaints the
// Add/Added states after the list changes elsewhere; open() loads the catalog.
function gamePicker(has, add) {
  const pk = { has, add, seq: 0, timer: 0, lastQ: '', results: [] };
  pk.el = el(`<div class="picker">
    <input class="input pk-search" type="search" placeholder="Search all Twitch categories" aria-label="Search all Twitch categories">
    <div class="pk-sec"><div class="pk-head"><span class="caption">Drops right now</span><span class="pk-time" data-cat-time></span><button class="linkbtn pk-refresh" type="button">Refresh</button></div><div class="pk-list" data-cat></div></div>
    <div class="pk-sec" data-search-sec hidden><div class="pk-head"><span class="caption">Search results</span></div><div class="pk-list" data-res></div></div>
    <div class="gadd"><input class="input pk-free" placeholder="Not listed? Exact game name" aria-label="Add a game by exact name"><button class="btn" type="button" data-pk-free>Add</button></div>
  </div>`);
  pk.refresh = () => { renderCatalogList(pk); renderSearchList(pk, pk.results, pk.lastQ); };
  pk.open = () => { renderCatalogList(pk); loadCatalog().then(() => renderCatalogList(pk)); };
  pk.el.querySelector('.pk-search').addEventListener('input', () => onPickerInput(pk));
  // Refresh keeps showing the current list until the new one arrives.
  pk.el.querySelector('.pk-refresh').addEventListener('click', () => { loadCatalog(true).then(() => refreshPickers()); });
  pk.el.addEventListener('click', e => { const b = e.target.closest('[data-pk-add]'); if (b) pickerAdd(pk, b.dataset.pkAdd); });
  const free = pk.el.querySelector('.pk-free');
  const addFree = () => { pickerAdd(pk, free.value); free.value = ''; free.focus(); };
  pk.el.querySelector('[data-pk-free]').addEventListener('click', addFree);
  free.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addFree(); } });
  pk.el.pickerRef = pk;
  return pk;
}

// refreshPickers repaints every mounted picker's Add/Added states.
function refreshPickers() {
  document.querySelectorAll('.picker').forEach(n => { if (n.pickerRef) n.pickerRef.refresh(); });
}

function mountSharedPicker() {
  const host = document.querySelector('#games-picker');
  if (!host) return;
  const pk = gamePicker(g => gIndex(peGames(), g) >= 0, g => { peGames().push(g); renderGamesCtl(); });
  host.append(pk.el);
  pk.open();
}

// ---- profile settings state (Games, Platforms and Profiles panes) ----

// profileEdits is a mutable clone of GET /api/profiles (every profile's own
// settings); profileSnapshot the untouched copy, so saveSettings sends only
// the profiles that changed and the panes know when they hold unsaved edits.
let profileEdits = null;
let profileSnapshot = null;
let profileLive = {};

const cloneProfiles = (list) => (list || []).map(p => ({ ...p, games: [...(p.games || [])] }));

// syncProfileField records a value the server accepted outside Settings (the
// Queue view's order select, a game-section move) in the Settings copies of
// that profile: the snapshot always, the edit only when it did not hold its
// own change to that field.
function syncProfileField(name, field, value) {
  const o = (profileSnapshot || []).find(x => x.name === name);
  const e = (profileEdits || []).find(x => x.name === name);
  if (!o) return;
  const untouched = e && JSON.stringify(e[field]) === JSON.stringify(o[field]);
  o[field] = Array.isArray(value) ? [...value] : value;
  if (untouched) e[field] = Array.isArray(value) ? [...value] : value;
}
const profileRowDirty = (p) => {
  const o = (profileSnapshot || []).find(x => x.name === p.name);
  return !o || JSON.stringify(o) !== JSON.stringify(p);
};
const profilesDirty = () => !!profileEdits && !!profileSnapshot && JSON.stringify(profileEdits) !== JSON.stringify(profileSnapshot);
const resetProfileEdits = () => { if (profileSnapshot) profileEdits = cloneProfiles(profileSnapshot); };

// loadProfileEdits reloads GET /api/profiles. keep=true (after adding or
// removing a profile) keeps the unsaved edits of the other rows.
async function loadProfileEdits(keep = false) {
  const list = await api.profileConfigs();
  const edited = keep ? (profileEdits || []).filter(profileRowDirty) : [];
  profileSnapshot = cloneProfiles(list);
  profileEdits = cloneProfiles(list).map(p => edited.find(x => x.name === p.name) || p);
}

async function loadProfileLive() {
  const state = await api.state();
  const live = {};
  (state.profiles || []).forEach(p => { live[p.name] = p.platforms || []; });
  profileLive = live;
}

function platformRuntimeStatus(p) {
  if (!p || !p.enabled) return { cls: 'idle', label: 'off' };
  if (!p.up) {
    if (p.launchError) return { cls: 'off', label: 'failed to start' };
    if (p.wanted) return { cls: 'warn', label: 'starting' };
    return p.needsLogin ? { cls: 'warn', label: 'needs sign-in' } : { cls: 'idle', label: 'stopped' };
  }
  if (p.needsLogin) return { cls: 'warn', label: 'needs sign-in' };
  return { cls: 'ok', label: 'running' };
}

function renderProfilesPane(host) {
  if (!host) return;
  host.innerHTML = `
    <div class="group"><div class="caption">Profiles</div><div class="card" id="prof-list"></div></div>
    <div class="group"><div class="caption">Add profile</div><div class="card">
      <div class="row stacked">
        <div class="lab"><div class="l">New profile</div><div class="h">Name: lowercase letters, digits and hyphens, 1-24 characters. Each profile has its own Chrome and sign-ins, and its own Games and Platforms settings (pick the profile at the top of those panes).</div></div>
        <div class="ctl" style="display:flex;gap:8px;flex-wrap:wrap">
          <input class="input" id="np-name" placeholder="name, e.g. profile-two" aria-label="Profile name" style="flex:1;min-width:140px">
          <input class="input" id="np-display" placeholder="Display name (optional)" aria-label="Display name" style="flex:1;min-width:140px">
          <button class="btn" id="np-add" type="button">Add profile</button>
        </div>
      </div>
    </div></div>`;
  renderProfileRows(host);
  host.querySelector('#np-add').addEventListener('click', () => addProfileFlow(host));
  markDirty();
}

// profileStatusHTML is one Profiles row's live per-platform status line.
function profileStatusHTML(name, enabled) {
  const livePlats = profileLive[name] || [];
  const tw = platformRuntimeStatus(livePlats[0] || { enabled });
  const kickLive = livePlats[1] || { enabled: false, up: false };
  const kk = platformRuntimeStatus(kickLive);
  return `<span><span class="fdot ${tw.cls}"></span>Twitch ${esc(tw.label)}</span>` +
    (kickLive.enabled || kickLive.up ? `<span><span class="fdot ${kk.cls}"></span>Kick ${esc(kk.label)}</span>` : '');
}

function profileRowEl(p, onlyOne, host) {
  const dn = p.displayName || p.name;
  const row = el(`<div class="row stacked" data-prof="${esc(p.name)}">
    <div style="display:flex;align-items:center;gap:14px;flex-wrap:wrap">
      <input class="input" data-pf="displayName" value="${esc(p.displayName)}" placeholder="${esc(p.name)}" aria-label="Display name for ${esc(p.name)}" style="width:160px">
      <span class="mono muted">${esc(p.name)}</span>
      <div style="flex:1"></div>
      <div class="inl inl-tog">Enabled ${toggleHTML('enabled', p.enabled, 'Enable ' + dn)}</div>
    </div>
    <div class="inl-status">${profileStatusHTML(p.name, p.enabled)}</div>
    <div data-pf-remove style="display:flex;justify-content:flex-end;align-items:center;gap:8px"></div>
  </div>`);
  const sw = row.querySelector('[data-toggle]');
  const flip = () => {
    const on = !sw.classList.contains('on');
    sw.classList.toggle('on', on);
    sw.setAttribute('aria-checked', String(on));
    p.enabled = on;
    markDirty();
  };
  sw.addEventListener('click', e => { e.preventDefault(); flip(); });
  sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); } });
  // The words next to the switch toggle it too.
  sw.closest('.inl-tog').addEventListener('click', e => { if (!sw.contains(e.target)) flip(); });
  row.querySelector('[data-pf="displayName"]').addEventListener('input', e => { p.displayName = e.target.value; markDirty(); });
  attachRemoveControl(row.querySelector('[data-pf-remove]'), p, onlyOne, host);
  return row;
}

function renderProfileRows(host) {
  const list = host.querySelector('#prof-list');
  if (!list) return;
  list.innerHTML = '';
  const onlyOne = (profileEdits || []).length <= 1;
  (profileEdits || []).forEach(p => list.append(profileRowEl(p, onlyOne, host)));
}

// attachRemoveControl: inline two-step confirm; disabled for the only profile.
function attachRemoveControl(wrap, p, onlyOne, host) {
  if (!wrap) return;
  const btn = el(`<button class="btn ghost danger" type="button" ${onlyOne ? 'disabled title="Add another profile before removing this one"' : ''}>Remove profile</button>`);
  let confirming = false;
  let cancelBtn = null;
  const reset = () => {
    confirming = false;
    btn.textContent = 'Remove profile';
    if (cancelBtn) { cancelBtn.remove(); cancelBtn = null; }
  };
  btn.addEventListener('click', async () => {
    if (onlyOne) return;
    if (!confirming) {
      confirming = true;
      btn.textContent = `Really remove ${p.displayName || p.name}?`;
      cancelBtn = el('<button class="btn ghost" type="button">Cancel</button>');
      cancelBtn.addEventListener('click', reset);
      wrap.append(cancelBtn);
      return;
    }
    try {
      const r = await api.removeProfile(p.name);
      toast(`Profile removed. Its queue, history and sign-ins are still on disk at ${r.dataDir}.`);
      await Promise.all([loadProfileEdits(true), loadProfileLive()]);
      if (settingsPane === 'profiles') { renderProfileRows(host); markDirty(); }
    } catch (e) {
      reset();
      toast(errText(e), 'err');
    }
  });
  wrap.append(btn);
}

async function addProfileFlow(host) {
  const nameInp = host.querySelector('#np-name');
  const dispInp = host.querySelector('#np-display');
  const name = (nameInp.value || '').trim().toLowerCase();
  if (!/^[a-z0-9-]{1,24}$/.test(name)) {
    toast('Name must be lowercase letters, digits and hyphens (1-24 characters).', 'err');
    nameInp.focus();
    return;
  }
  try {
    await api.addProfile(name, dispInp.value.trim());
    toast('Profile added. Press Log in on its Dashboard card to sign in, then Start.');
    nameInp.value = ''; dispInp.value = '';
    await Promise.all([loadProfileEdits(true), loadProfileLive()]);
    if (settingsPane === 'profiles') { renderProfileRows(host); markDirty(); }
    // Poll briefly so the new row's live status appears; only the status
    // lines change, so typing in a row is never interrupted.
    for (let i = 0; i < 4 && settingsPane === 'profiles'; i++) {
      await sleep(1000);
      await loadProfileLive();
      if (settingsPane !== 'profiles') break;
      host.querySelectorAll('[data-prof]').forEach(r => {
        const p = (profileEdits || []).find(x => x.name === r.dataset.prof);
        r.querySelector('.inl-status').innerHTML = profileStatusHTML(r.dataset.prof, p ? p.enabled : true);
      });
    }
  } catch (e) {
    toast(errText(e), 'err');
  }
}

// refreshDiscovery asks every running, unpaused engine for a discovery pass
// and returns how many accepted the request.
async function refreshDiscovery() {
  const calls = [];
  try {
    const profiles = await api.profiles();
    for (const prof of profiles) {
      for (const p of (prof.platforms || [])) {
        if (p.up && !(p.status && p.status.paused)) calls.push(api.engine(p.platform, prof.name).refresh());
      }
    }
  } catch (_) { /* nothing running */ }
  const res = await Promise.allSettled(calls);
  return res.filter(r => r.status === 'fulfilled').length;
}

// ---- Debugging view ----

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; }
  catch (e) {
    try {
      const ta = document.createElement('textarea');
      ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta); ta.focus(); ta.select();
      const ok = document.execCommand('copy'); document.body.removeChild(ta); return ok;
    } catch (e2) { return false; }
  }
}

async function copyDiagnostics(btn) {
  btn.disabled = true;
  try {
    const text = await api.diagnostics();
    const ok = await copyText(text);
    toast(ok ? 'Diagnostics copied. Paste them into your bug report.' : 'Copy failed', ok ? 'ok' : 'err');
  } catch (e) { toast(errText(e), 'err'); }
  btn.disabled = false;
}

async function exportDiagnostics(btn) {
  btn.disabled = true;
  try {
    const r = await api.exportDiagnostics();
    const out = $('#dbg-export');
    if (out) out.innerHTML = `Saved to <span class="mono">${esc(r.path)}</span>`;
    toast('Diagnostics saved');
  } catch (e) { toast(errText(e), 'err'); }
  btn.disabled = false;
}

async function flipDumpToggle(sw) {
  const on = !sw.classList.contains('on');
  sw.classList.toggle('on', on);
  sw.setAttribute('aria-checked', String(on));
  // Save on a fresh copy of the server config so this never commits unsaved
  // edits sitting in the Settings form.
  try {
    await api.refreshState();
    const c = await loadCfg();
    c.captureGqlBodies = on;
    await api.saveConfig(c);
    if (cfg) cfg.captureGqlBodies = on;
    if (cfgSnapshot) { const b = snap(); b.captureGqlBodies = on; cfgSnapshot = JSON.stringify(b); }
    toast(on ? 'Saving raw responses' : 'Stopped saving raw responses');
  } catch (e) {
    sw.classList.toggle('on', !on);
    sw.setAttribute('aria-checked', String(!on));
    toast(errText(e), 'err');
  }
}

function debugShellEl() {
  const shell = el(`<div>
    <div><h2 class="title">Debugging</h2><p class="sub">What Dropwarden is doing right now. Diagnostics include settings and recent log lines, never passwords or sign-in tokens.</p></div>
    <div class="foot-btns dbg-main">
      <button class="btn primary" id="dbg-diag" type="button">Copy diagnostics</button>
      <button class="btn" id="dbg-export-btn" type="button">Export diagnostics</button>
      <button class="btn ghost" id="dbg-discover" type="button">Check for drops now</button>
      <button class="btn ghost" id="dbg-refresh" type="button">Refresh</button>
    </div>
    <div id="dbg-export" class="save-msg" aria-live="polite"></div>
    <details class="adv"><summary>Advanced (troubleshooting)</summary><div class="card">
      ${row('Save raw Twitch responses', 'Writes the raw responses to the debug folder for bug reports. Leave off otherwise.', toggleHTML('captureGqlBodies', !!(cfg && cfg.captureGqlBodies), 'Save raw Twitch responses'))}
      ${row('Raw state', 'Everything on this page as JSON.', '<button class="btn btn-sm" id="dbg-copy-json" type="button">Copy</button>')}
    </div></details>
    <div id="debug-panel"></div>
  </div>`);
  shell.querySelector('#dbg-diag').addEventListener('click', e => copyDiagnostics(e.currentTarget));
  shell.querySelector('#dbg-export-btn').addEventListener('click', e => exportDiagnostics(e.currentTarget));
  shell.querySelector('#dbg-refresh').addEventListener('click', renderDebugPanel);
  shell.querySelector('#dbg-discover').addEventListener('click', async () => {
    const n = await refreshDiscovery();
    if (n > 0) { toast('Checking for drops'); setTimeout(renderDebugPanel, 1000); }
    else toast('Nothing is running. Press Start or Resume first.', 'err');
  });
  shell.querySelector('#dbg-copy-json').addEventListener('click', async () => {
    const ok = await copyText(JSON.stringify(lastDebug || {}, null, 2));
    toast(ok ? 'Copied' : 'Copy failed', ok ? 'ok' : 'err');
  });
  const sw = shell.querySelector('[data-toggle="captureGqlBodies"]');
  sw.addEventListener('click', () => flipDumpToggle(sw));
  sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flipDumpToggle(sw); } });
  return shell;
}

async function renderDebug() {
  const v = $('#v-debug');
  if (!v) return;
  if (!cfg) cfg = await loadCfg().catch(() => null);
  if (!v.querySelector('#debug-panel')) { v.innerHTML = ''; v.append(debugShellEl()); }
  const sw = document.querySelector('[data-toggle="captureGqlBodies"]');
  if (sw && cfg) { sw.classList.toggle('on', !!cfg.captureGqlBodies); sw.setAttribute('aria-checked', String(!!cfg.captureGqlBodies)); }
  await renderDebugPanel();
}

function debugPlatformCard(name, p) {
  const st = p.status || {};
  const elig = p.eligibility || {};
  const eligStr = Object.keys(elig).length ? Object.entries(elig).map(([k, v]) => `${cap(k)} ${v}`).join('   ') : '-';
  const runLine = p.up
    ? `${esc(st.paused ? 'Paused' : labelOf(STATE_LABEL, st.state || 'idle'))}${st.channelLogin ? ' - ' + esc(st.channelLogin) : ''}`
    : (p.enabled ? 'Stopped' : 'Off');
  const disc = p.lastDiscovery;
  let discLine = 'not run yet';
  let extra = '';
  if (name === 'twitch') {
    if (disc) discLine = `responses ${disc.gqlResponses} - campaigns ${disc.campaignsParsed} - details ${disc.detailsParsed} - queued ${disc.enqueued}`;
    const token = ((disc && disc.headerKeys) || []).indexOf('client-integrity') !== -1;
    extra = `<div class="kv"><span class="k">Twitch security token seen</span><span class="v" style="color:${token ? 'var(--pill-ok)' : 'var(--pill-err)'}">${token ? 'yes' : 'no'}</span></div>`;
  } else if (disc) {
    discLine = `status ${disc.apiStatus || '-'} - campaigns ${disc.campaignsParsed} - queued ${disc.enqueued}`;
    if (disc.lastError) extra = `<div class="kv"><span class="k">Last error</span><span class="v" style="color:var(--pill-err)">${esc(disc.lastError)}</span></div>`;
  }
  return `<div class="card mini dbg-card">
    <h3>${pfBadge(name)} ${esc(platLabel(name))}</h3>
    <div class="kv"><span class="k">Status</span><span class="v">${runLine}</span></div>
    <div class="kv"><span class="k">Drops by eligibility</span><span class="v mono">${esc(eligStr)}</span></div>
    <div class="kv"><span class="k">Last check for drops</span><span class="v mono">${esc(discLine)}</span></div>
    ${extra}
  </div>`;
}

async function renderDebugPanel() {
  const host = document.querySelector('#debug-panel');
  if (!host) return;
  let d;
  try { d = await api.debug(); lastDebug = d; }
  catch (e) { host.innerHTML = '<div class="card mini"><div class="empty">Debug info needs the app running (this is a preview).</div></div>'; return; }
  const names = Object.keys(d.profiles || {});
  if (!debugProfile || !names.includes(debugProfile)) debugProfile = names[0] || '';
  const name = debugProfile;
  const prof = (d.profiles || {})[name] || {};
  const paths = ((d.paths || {}).profiles || {})[name] || {};
  const logText = (d.logTail || []).slice(-120).join('\n') || 'no log lines yet';
  let pills = '';
  if (names.length > 1) {
    pills = `<div class="prof-pills" role="group" aria-label="Profile">` + names.map(n => {
      const dn = ((d.profiles || {})[n] || {}).displayName || n;
      return `<button type="button" class="prof-pill ${n === name ? 'active' : ''}" aria-pressed="${n === name}" data-dbgprof="${esc(n)}">${esc(dn)}</button>`;
    }).join('') + `</div>`;
  }
  let cards = debugPlatformCard('twitch', prof.twitch || {});
  if (prof.kick && prof.kick.enabled) cards += debugPlatformCard('kick', prof.kick);
  // Built once, then only the parts that changed are repainted: the log keeps
  // its scroll position and any text selection, and focus survives.
  if (!host.querySelector('#dbg-log')) {
    host.innerHTML = `<div id="dbg-top"></div><div class="card mini">
      <div class="kv"><span class="k">Debug folder</span><span class="v mono path" id="dbg-dir"></span></div>
      <div class="log-head"><div class="caption" style="margin:0">Recent log</div><button class="btn ghost btn-sm" id="dbg-log-copy" type="button">Copy log</button></div>
      <pre id="dbg-log" class="log-pre" tabindex="0"></pre>
    </div>`;
    host.querySelector('#dbg-top').addEventListener('click', e => {
      const b = e.target.closest('[data-dbgprof]');
      if (!b) return;
      debugProfile = b.dataset.dbgprof;
      renderDebugPanel();
    });
    host.querySelector('#dbg-log-copy').addEventListener('click', async () => {
      const ok = await copyText(host.querySelector('#dbg-log').textContent);
      toast(ok ? 'Log copied' : 'Copy failed', ok ? 'ok' : 'err');
    });
  }
  const top = host.querySelector('#dbg-top');
  if (top.dwKey !== pills + cards) {
    const fk = focusKey(top);
    top.innerHTML = pills + cards;
    top.dwKey = pills + cards;
    restoreFocus(top, fk);
  }
  const dir = host.querySelector('#dbg-dir');
  dir.textContent = paths.debugDir || '';
  dir.title = paths.debugDir || '';
  const logEl = host.querySelector('#dbg-log');
  const sel = window.getSelection();
  const selecting = !!sel && !sel.isCollapsed && logEl.contains(sel.anchorNode);
  if (logEl.textContent !== logText && !selecting) {
    // Follow new lines only when the user is already at (or near) the end.
    const atEnd = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 20;
    logEl.textContent = logText;
    if (atEnd) logEl.scrollTop = logEl.scrollHeight;
  }
}

// ---- shell ----

function cap(s) { return String(s || '').replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase()); }
function fmtDate(d) {
  if (!d) return '';
  if (typeof d === 'string' && !d.includes('T')) return d; // sample pre-formatted
  const dt = new Date(d);
  return isNaN(dt) ? String(d) : dt.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

async function refreshAll() {
  await Promise.all([renderFoot(), renderDashboard(), renderQueue(), renderHistory()]);
}

// go switches to a view and renders it; leaving Settings with unsaved edits
// asks first.
async function go(view) {
  if (isActive('settings') && view === 'settings') return;
  if (isActive('settings') && isDirty()) {
    const c = await confirmLeave();
    if (c === 'cancel') return;
    if (c === 'save' && !(await saveSettings())) return;
    if (c === 'discard') discardSettings();
  }
  switchView(view);
  setRail(false);
  if (view === 'queue') renderQueue();
  else if (view === 'history') renderHistory();
  else if (view === 'settings') renderSettings();
  else if (view === 'debug') renderDebug();
  else renderDashboard();
}

// queueBusy: skip the queue poll re-render while the user is mid-drag or has
// just opened the priority dropdown (no choice made yet, at most 10 s). Focus
// anywhere else in the list survives the re-render (focusKey/restoreFocus).
function queueBusy() {
  if (dragRow) return true;
  const a = document.activeElement;
  return !!(a && a.id === 'prio' && !prioChanged && Date.now() - prioOpenedAt < 10000);
}

// setRail opens or closes the narrow-window rail overlay and keeps the menu
// button's aria-expanded in step.
function setRail(open) {
  $('.app').classList.toggle('rail-open', open);
  const tog = $('#rail-toggle');
  if (tog) tog.setAttribute('aria-expanded', String(open));
}

function initShell() {
  document.querySelectorAll('[data-icon]').forEach(n => { n.innerHTML = icon(n.dataset.icon, 14); });
  document.querySelectorAll('.nav').forEach(n => n.addEventListener('click', () => go(n.dataset.view)));
  const tog = $('#rail-toggle');
  if (tog) tog.addEventListener('click', () => setRail(!$('.app').classList.contains('rail-open')));
  // The open rail closes on Escape or a press anywhere outside it.
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || e.defaultPrevented || modalClose || !$('.app').classList.contains('rail-open')) return;
    setRail(false);
    if (tog) tog.focus();
  });
  document.addEventListener('mousedown', e => {
    if ($('.app').classList.contains('rail-open') && !e.target.closest('.rail')) setRail(false);
  });
  // Any "Choose games" link anywhere opens Settings > Games, for the profile
  // the link names when it names one.
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-choose-games]');
    if (!b) return;
    if (b.dataset.profile) selectedProfile = b.dataset.profile;
    openSettings('games');
  });
}

function init() {
  // The last saved theme until the config arrives (none = follow the OS).
  applyTheme(LS.get('dw.theme'));
  initShell();
  loadCfg().then(c => {
    cfg = c;
    takeSnapshot();
    applyTheme(cfg && cfg.theme, !api.offline);
    refreshAll();
  }).catch(() => refreshAll());
  // Live updates: re-poll the active view periodically, but not while the
  // window is hidden (tray-first: that is most of the time).
  setInterval(() => {
    if (document.hidden) return;
    if (isActive('dashboard')) renderDashboard();
    else {
      if (isActive('queue') && !queueBusy()) renderQueue();
      else if (isActive('history')) renderHistory();
      // Keep update state (tray promptSeq, deferral) fresh off the dashboard.
      api.state().then(s => { if (s.update) onUpdate(s.update); }).catch(() => {});
    }
    renderFoot();
  }, 5000);
  setInterval(() => { if (!document.hidden && isActive('debug')) renderDebugPanel(); }, 4000);
  // Shown again from the tray: repaint at once instead of waiting for a poll.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    refreshAll();
    if (isActive('debug')) renderDebugPanel();
  });
}

function isActive(view) {
  const s = document.querySelector('.view[data-view="' + view + '"]');
  return !!(s && s.classList.contains('active'));
}

init();
