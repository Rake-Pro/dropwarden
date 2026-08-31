import { api } from '/api.js';

const $ = (s, r = document) => r.querySelector(s);
const el = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const pct = (cur, req) => req > 0 ? Math.min(100, Math.round(cur / req * 100)) : 0;

let cfg = null;

// selectedProfile drives the Queue/History/Settings profile pill bar: a
// module-level var defaulting to the first (enabled) profile, no persistence
// (see pickProfile). The Dashboard is unaffected - it shows every enabled
// profile at once, not a single selected one.
let selectedProfile = null;

// Platform display metadata. Everything platform-facing renders through this
// table so a future platform is one row here, not a scatter of special cases.
const PLATFORM_META = {
  twitch: { label: 'Twitch' },
  kick: { label: 'Kick' },
};
const platLabel = (p) => (PLATFORM_META[p] || { label: p }).label;
const pfBadge = (p) => PLATFORM_META[p] ? `<span class="pf ${esc(p)}">${esc(platLabel(p).toUpperCase())}</span>` : '';
// profBadge marks up-next/queue rows with their profile when more than one
// profile is enabled - the profile equivalent of pfBadge above.
const profBadge = (name) => `<span class="pf profile">${esc(name)}</span>`;

// pickProfile resolves the profile the Queue/History/Settings views render:
// enabled profiles only, keyed by selectedProfile (defaulting to and falling
// back to the first one, e.g. after a profile is disabled mid-session).
function pickProfile(state) {
  const profiles = (state.profiles || []).filter(p => p.enabled);
  if (!profiles.length) return { profiles, profile: null, multiProfile: false };
  if (!selectedProfile || !profiles.some(p => p.name === selectedProfile)) selectedProfile = profiles[0].name;
  const profile = profiles.find(p => p.name === selectedProfile) || profiles[0];
  return { profiles, profile, multiProfile: profiles.length > 1 };
}

// profilePillBar renders the profile selector shown in the Queue/History/
// Settings headers once there is more than one profile; onPick re-renders
// the calling view after the selection changes.
function profilePillBar(profiles, onPick) {
  const bar = el(`<div class="prof-pills"></div>`);
  profiles.forEach(p => {
    const pill = el(`<button type="button" class="prof-pill ${p.name === selectedProfile ? 'active' : ''}">${esc(p.displayName)}</button>`);
    pill.addEventListener('click', () => { selectedProfile = p.name; onPick(); });
    bar.append(pill);
  });
  return bar;
}

// visiblePlatforms/isMulti: a platform is shown when it is enabled OR its
// engine is still running (just disabled in settings, still winding down) - a
// running engine must never lose its controls. Badges and per-platform
// breakdowns appear only when more than one platform is visible.
const visiblePlatforms = (plats) => (plats || []).filter(p => p.enabled || p.up);
const isMulti = (plats) => visiblePlatforms(plats).length > 1;

// loadCfg returns a mutable copy of the config so form edits never leak into
// the shared state cache (or get committed by an unrelated save elsewhere).
async function loadCfg() {
  const c = await api.config();
  return { ...c, games: [...(c.games || [])] };
}

function applyTheme(theme) {
  if (theme && theme !== 'system') document.documentElement.setAttribute('data-theme', theme);
  else document.documentElement.removeAttribute('data-theme');
}

function switchView(view) {
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.dataset.view === view));
  document.querySelectorAll('.nav').forEach(n => n.classList.toggle('active', n.dataset.view === view));
}

// footRow renders one (profile, platform) footer line. With one profile the
// label collapses to today's platform-only text; with more than one it gets
// a "Greg - Twitch" prefix.
function footRow(row, displayName, multiProfile, p) {
  const label = multiProfile ? `${displayName} - ${platLabel(p.platform)}` : platLabel(p.platform);
  if (!p.up) {
    // Chrome launches only while a watcher runs, so a down platform is stopped
    // by design (Start closes/opens the window) - unless a run intent is live,
    // in which case it is launching or its launch failed. Offer the login hint
    // when it has never signed in.
    if (p.wanted) return row('warn', label + ': launching (check Debugging if stuck)');
    return p.needsLogin ? row('warn', label + ' needs login') : row('off', label + ' stopped');
  }
  const st = p.status || {};
  // Up but never logged in: the window is parked on the login page.
  if (p.needsLogin) return row('warn', label + ' logging in');
  if (st.paused) return row('warn', label + ' paused');
  return row('ok', label + ' running');
}

async function renderFoot() {
  const state = await api.state();
  const profiles = state.profiles || [];
  const multiProfile = profiles.length > 1;
  const foot = $('#rail-foot');
  const row = (cls, label) => `<div class="frow"><span class="fdot ${cls}"></span>${label}</div>`;
  if (api.offline) {
    foot.innerHTML = row('off', 'Preview mode') + row('off', 'Backend not connected');
    return;
  }
  let rows = '';
  for (const prof of profiles) {
    for (const p of visiblePlatforms(prof.platforms)) {
      rows += footRow(row, prof.displayName, multiProfile, p);
    }
  }
  foot.innerHTML = rows || row('warn', 'No profiles configured');
}

// nowCardEl renders one platform's "Now watching" card for a profile.
function nowCardEl(p, multi, profile) {
  const st = p.status || {};
  const state = p.up ? (st.state || 'idle') : 'offline';
  const watching = state === 'watching';
  // Transitional engine states get real copy instead of the idle empty-state.
  const busyCopy = {
    discovering: 'Scanning campaigns and inventory…',
    selecting_channel: 'Choosing a channel to watch…',
    opening: `Opening ${st.channelLogin || 'channel'}…`,
    validating_playback: `Validating playback on ${st.channelLogin || 'the channel'}…`,
    claiming: 'Claiming completed drop…',
    cooldown: 'Cooling down before the next attempt…',
    backoff: 'Recovering — backing off before the next retry…',
    needs_user_action: 'Action needed — check the queue for details.',
  };
  // needs_user_action gets busyCopy text but must NOT read as in-progress.
  const busy = Object.prototype.hasOwnProperty.call(busyCopy, state) && state !== 'needs_user_action';
  const showChannel = watching || state === 'opening' || state === 'validating_playback';
  const headName = showChannel && st.channelLogin ? st.channelLogin : 'idle';
  const badge = multi ? pfBadge(p.platform) : '';
  const disabled = api.offline ? 'disabled' : '';
  const volPct = Math.round(((cfg && cfg.volume) || 0.01) * 100);

  // Chrome launches only while a watcher runs, so a down platform is stopped
  // (Start opens the window, Stop closes it), not mid-launch.
  const stopped = !p.up;

  let body;
  if (stopped && p.needsLogin) {
    body = `<div class="empty" style="text-align:left;padding:18px 2px">
      <p style="margin:0 0 12px">${esc(platLabel(p.platform))} is not signed in yet. Open the login window, sign in, then press Start.</p>
    </div>`;
  } else if (stopped && p.wanted) {
    body = `<div class="empty">Launching Chrome... if this persists, the launch failed - check the Debugging view and the log.</div>`;
  } else if (stopped) {
    body = `<div class="empty">Stopped. Press Start to launch Chrome and begin working the queue.</div>`;
  } else if (p.needsLogin) {
    // Mode-agnostic wording: in shared-window mode Kick is a TAB in Twitch's
    // window, so this must never promise a separate "new window" for Kick.
    body = `<div class="empty" style="text-align:left;padding:18px 2px">
      <p style="margin:0 0 12px">Log in to ${esc(platLabel(p.platform))} in this profile's watcher window, then check.</p>
      <button class="btn" data-act="check-login" ${disabled}>Check login</button>
      <div class="login-msg" style="margin-top:10px;font-size:12px;min-height:1.2em"></div>
    </div>`;
  } else if (watching) {
    body = `<div class="drop-row">
      <div class="lbl"><span class="name">${esc(st.dropName)}</span><span class="mins mono">${st.current} / ${st.required} min</span></div>
      <div class="bar"><i class="${p.platform === 'kick' ? 'kick' : ''}" style="width:${pct(st.current, st.required)}%"></i></div>
    </div>
    <div class="watchdogs">
      ${st.current > 0
        ? '<span class="wd"><span class="tick">✓</span> Progress ticking</span>'
        : '<span class="wd"><span class="tick wait">◌</span> Awaiting first tick</span>'}
      <span class="wd"><span class="tick">✓</span> Playback validated</span>
      <span class="wd"><span class="tick">✓</span> Volume ${volPct}%</span>
    </div>`;
  } else if (busy || state === 'needs_user_action') {
    body = `<div class="empty">${esc(busyCopy[state])}</div>`;
  } else {
    body = `<div class="empty">Not watching. Press ${st.paused ? 'Resume' : 'Start'} to begin working the queue.</div>`;
  }

  // Down: Start launches the window (Log in opens it parked on the login page
  // first, the one launch with no watcher). Up: Stop closes the window, and a
  // paused engine can't run a requested discovery, so Refresh is disabled until
  // Start.
  let btns;
  if (stopped && p.needsLogin) {
    btns = `<div class="foot-btns">
      <button class="btn primary" data-act="login" ${disabled}>Log in</button>
      <button class="btn ghost" data-act="start" ${disabled}>Start anyway</button>
    </div>`;
  } else if (stopped) {
    btns = `<div class="foot-btns">
      <button class="btn primary" data-act="start" ${disabled}>Start</button>
    </div>`;
  } else {
    btns = `<div class="foot-btns">
      ${watching ? `<button class="btn" data-act="skip" ${disabled}>Skip drop</button>` : ''}
      ${state === 'idle' ? `<button class="btn primary" data-act="start" ${disabled}>Start</button>` : ''}
      <button class="btn ghost" data-act="refresh" ${st.paused ? 'disabled title="paused — start to refresh"' : disabled}>Refresh campaigns</button>
      <button class="btn ghost danger" data-act="stop" ${disabled}>Stop</button>
    </div>`;
  }

  const card = el(`<div class="card now">
    <div class="head">
      <div class="avatar">${esc((showChannel && st.channelLogin ? st.channelLogin : '--').slice(0, 2).toUpperCase())}</div>
      <div class="who">
        <div class="ch"><span class="nm">${esc(headName)}</span> ${badge} ${watching ? '<span class="live">LIVE</span>' : ''}</div>
        <div class="game">${esc(st.game || '')}${p.up && !p.enabled ? ' · disabled in settings, stopping' : ''}</div>
      </div>
      <span class="pill ${esc(state)} ${busy ? 'busy' : ''}"><span class="d"></span> ${esc(cap(state === 'offline' ? 'not running' : state))}</span>
    </div>
    ${body}
    ${btns}
  </div>`);
  card.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', async () => {
    const act = b.dataset.act;
    if (act === 'check-login') {
      const out = card.querySelector('.login-msg');
      if (out) { out.textContent = 'Checking...'; out.style.color = 'var(--text-secondary)'; }
      try {
        const r = await api.loginCheck(profile, p.platform);
        if (out) {
          if (r.loggedIn === true) {
            // A confirmed login auto-starts the watcher backend-side; reflect it.
            out.textContent = 'Logged in - starting the watcher.'; out.style.color = 'var(--ok)';
            setTimeout(() => refreshAll(), 1200);
          }
          else if (r.loggedIn === false) { out.textContent = 'Not logged in yet - finish logging in and check again.'; out.style.color = 'var(--warn)'; }
          else {
            const why = r.detail ? ` [probe: ${r.detail}]` : '';
            out.textContent = `Can't auto-detect login for ${platLabel(p.platform)}${why} - once you're logged in, press Start (or Stop to close the window until later).`;
            out.style.color = 'var(--text-secondary)';
          }
        }
      } catch (e) {
        if (out) { out.textContent = 'Check failed: ' + (e.message || e); out.style.color = 'var(--err)'; }
      }
      return; // no engine action, and no re-render - keep the result visible
    }
    const a = api.engine(p.platform, profile);
    try {
      if (act === 'start') await a.start();
      if (act === 'login') await a.login();
      if (act === 'stop') await a.stop();
      if (act === 'skip') await a.skip();
      if (act === 'refresh') await a.refresh();
    } catch (_) { /* surfaced by the next poll */ }
    refreshAll();
  }));
  return card;
}

// needyBanner renders the "drops need your attention" banner. profileName is
// set only in the multi-profile per-section form, so its Open queue link also
// selects that profile's pill.
function needyBanner(needy, profileName) {
  const detail = needy.slice(0, 2).map(q => `${q.dropName} — ${q.reason || 'action required'}`).join('  ·  ')
    + (needy.length > 2 ? `  ·  +${needy.length - 2} more` : '');
  const banner = el(`<div class="banner needs">
    <span class="ic">!</span>
    <div class="tx"><div class="t">${needy.length === 1 ? 'A drop needs' : needy.length + ' drops need'} your attention</div>
    <div class="d">${esc(detail)}</div></div>
    <button class="btn btn-sm" data-goto="queue">Open queue</button>
  </div>`);
  banner.querySelector('[data-goto]').addEventListener('click', () => {
    if (profileName) selectedProfile = profileName;
    go('queue');
  });
  return banner;
}

// combineStats sums headline stats across enabled profiles for the shared
// Totals card; successRate is a session-weighted average, not a plain mean.
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

async function renderDashboard() {
  const state = await api.state();
  const allProfiles = state.profiles || [];
  // Visible mirrors the platform rule: enabled, OR still running while a
  // just-disabled profile's engines wind down.
  const profiles = allProfiles.filter(p => p.enabled || (p.platforms || []).some(pl => pl.up));
  const multiProfile = profiles.length > 1;

  const v = $('#v-dashboard');
  v.innerHTML = '';

  const anyMulti = profiles.some(p => isMulti(p.platforms));
  v.append(el(`<div>
    <h2 class="title">Now watching</h2>
    <p class="sub">${anyMulti
      ? 'One channel per platform. The watcher keeps each window unfocused and dimmed while progress ticks.'
      : 'One channel at a time. The watcher keeps this window unfocused and dimmed while progress ticks.'}</p>
  </div>`));

  const left = el(`<div class="stack"></div>`);

  if (!multiProfile) {
    // Single-profile install: identical to the original single-profile view -
    // no person header, no per-section banner (a plain global one instead).
    const prof = profiles[0] || { name: '', queue: [], platforms: [] };
    const plats = visiblePlatforms(prof.platforms);
    const multi = isMulti(prof.platforms);
    const needy = (prof.queue || []).filter(q => q.status === 'needs_action');
    if (needy.length) v.append(needyBanner(needy));
    for (const p of plats) left.append(nowCardEl(p, multi, prof.name));
  } else {
    for (const prof of profiles) {
      const plats = visiblePlatforms(prof.platforms);
      const multi = isMulti(prof.platforms);
      const needy = (prof.queue || []).filter(q => q.status === 'needs_action');
      const section = el(`<div class="profile-section"><div class="profile-head"><h3 class="pname">${esc(prof.displayName)}</h3></div></div>`);
      if (needy.length) section.append(needyBanner(needy, prof.name));
      const row = el(`<div class="now-row" style="display:grid;grid-template-columns:repeat(${Math.max(plats.length, 1)}, minmax(0, 1fr));gap:14px"></div>`);
      for (const p of plats) row.append(nowCardEl(p, multi, prof.name));
      section.append(row);
      left.append(section);
    }
  }

  // Up next: a single shared list across every enabled profile's pending
  // queue, badging the profile (and platform, per that profile's own
  // multi-platform-ness) once there is more than one profile.
  const upnext = multiProfile
    ? profiles.flatMap(p => (p.queue || []).filter(q => q.status === 'pending')
        .map(q => ({ ...q, profName: p.displayName, profPlatMulti: isMulti(p.platforms) })))
      .slice(0, 3)
    : ((profiles[0] && profiles[0].queue) || []).filter(q => q.status === 'pending').slice(0, 3);
  const soloMulti = !multiProfile && isMulti((profiles[0] || {}).platforms);
  const upnextHTML = upnext.length ? upnext.map((q, i) => {
    const platBit = (multiProfile ? q.profPlatMulti : soloMulti) ? pfBadge(q.platform) + ' ' : '';
    const profBit = multiProfile ? profBadge(q.profName) + ' ' : '';
    return `<div class="up"><span class="idx">${i + 1}</span><div class="t"><div class="n">${profBit}${platBit}${esc(q.dropName)}</div><div class="g">${esc(q.game)} · ${esc(q.reason || '')}</div></div><span class="m mono">${q.currentMinutes}/${q.requiredMinutes}</span></div>`;
  }).join('') : '<div class="empty">Queue is empty.</div>';

  // Totals: shared aggregate headline numbers, plus a byPlatform breakdown
  // (single profile with >1 platform, today's behavior) or a per-profile
  // breakdown (>1 profile) below them - never both.
  const stats = multiProfile ? combineStats(profiles) : ((profiles[0] && profiles[0].stats) || {});
  const totalsRows = multiProfile
    ? profiles.map(p => `<div class="kv"><span class="k">${esc(p.displayName)}</span><span class="v">${((p.stats || {}).minutesWatched) || 0} min · ${((p.stats || {}).dropsClaimed) || 0} claimed</span></div>`).join('')
    : (soloMulti ? visiblePlatforms((profiles[0] || {}).platforms).map(p => {
        const bp = (((profiles[0] || {}).stats || {}).byPlatform || {})[p.platform] || {};
        return `<div class="kv"><span class="k">${pfBadge(p.platform)}</span><span class="v">${bp.minutesWatched || 0} min · ${bp.dropsClaimed || 0} claimed</span></div>`;
      }).join('') : '');

  const side = el(`<div class="stack">
    <div class="card mini"><h3>Up next</h3><div class="upnext">${upnextHTML}</div></div>
    <div class="card mini"><h3>Totals</h3>
      <div class="kv"><span class="k">Drops earned</span><span class="v">${stats.dropsEarned || 0}</span></div>
      <div class="kv"><span class="k">Drops claimed</span><span class="v">${stats.dropsClaimed || 0}</span></div>
      <div class="kv"><span class="k">Minutes watched</span><span class="v">${stats.minutesWatched || 0}</span></div>
      <div class="kv"><span class="k">Sessions</span><span class="v">${stats.sessions || 0}</span></div>
      <div class="kv"><span class="k">Success rate</span><span class="v">${stats.successRate || 0}%</span></div>
      ${totalsRows}
    </div>
  </div>`);

  const grid = el(`<div class="grid2"></div>`);
  grid.append(left, side);
  v.append(grid);
}

async function renderQueue() {
  const state = await api.state();
  const { profiles, profile, multiProfile } = pickProfile(state);
  const prof = profile || { name: '', platforms: [], queue: [] };
  const multi = isMulti(prof.platforms);
  // Only current, workable items belong here; claimed/expired live in History.
  const queue = (prof.queue || []).filter(q => q.status !== 'claimed' && q.status !== 'expired');
  const v = $('#v-queue');
  v.innerHTML = '';
  v.append(el(`<div class="qhead">
    <div><h2 class="title">Queue</h2><p class="sub" style="margin:0">Drag to reorder. ${multi ? 'One shared list — each platform works its own drops top to bottom.' : 'The watcher works top to bottom.'}</p></div>
    <div style="display:flex;gap:8px;align-items:center;">
      <span style="color:var(--text-tertiary);font-size:11.5px">Priority</span>
      <select class="popup" id="prio">
        <option value="top_down">Queue order (top-down)</option>
        <option value="ending_soonest">Ending soonest</option>
        <option value="closest_to_complete">Closest to complete</option>
        <option value="shortest_first">Shortest first</option>
      </select>
    </div>
  </div>`));
  if (multiProfile) v.append(profilePillBar(profiles, () => renderQueue()));
  if (cfg) $('#prio', v).value = cfg.priorityMode;
  $('#prio', v).addEventListener('change', async e => {
    try {
      await api.setPriority(e.target.value);
      if (cfg) cfg.priorityMode = e.target.value; // else the next poll snaps the select back
    } catch (_) { if (cfg) e.target.value = cfg.priorityMode; }
  });

  const list = el(`<div class="card qlist"></div>`);
  if (!queue.length) list.append(el('<div class="empty">No drops queued yet. Run a discovery pass.</div>'));
  for (const q of queue) {
    const row = el(`<div class="qrow ${q.status === 'active' ? 'active' : ''}" data-id="${esc(q.dropID)}" draggable="true">
      <span class="grip" title="drag to reorder">⁝⁝</span>
      <div class="qname"><div class="n">${multi ? pfBadge(q.platform) + ' ' : ''}${esc(q.dropName || q.dropID)}</div><div class="meta">${esc(q.game)}${q.reason ? ' · ' + esc(q.reason) : ''}</div></div>
      <div class="qmini"><div class="bar"><i class="${q.status === 'active' ? (q.platform === 'kick' ? 'kick' : '') : 'blue'}" style="width:${pct(q.currentMinutes, q.requiredMinutes)}%"></i></div><div class="mm mono">${q.requiredMinutes ? q.currentMinutes + ' / ' + q.requiredMinutes + ' min' : '—'}</div></div>
      <span class="pill ${esc(q.status)}"><span class="d"></span> ${esc(cap(q.status))}</span>
      <div class="switch ${q.enabled ? 'on' : ''}" role="switch" aria-checked="${q.enabled}" tabindex="0"><i></i></div>
    </div>`);
    const sw = row.querySelector('.switch');
    const flipSwitch = async () => {
      const on = !sw.classList.contains('on');
      sw.classList.toggle('on', on);
      sw.setAttribute('aria-checked', String(on));
      try { await api.setEnabled(q.dropID, on, prof.name); }
      catch (_) { sw.classList.toggle('on', !on); sw.setAttribute('aria-checked', String(!on)); }
    };
    sw.addEventListener('click', flipSwitch);
    sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flipSwitch(); } });
    list.append(row);
  }
  v.append(list);
  if (queue.length > 1) attachQueueDrag(list, prof.name);
}

let dragRow = null;

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

function attachQueueDrag(list, profile) {
  list.querySelectorAll('.qrow').forEach(row => {
    row.addEventListener('dragstart', e => {
      dragRow = row;
      row.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      try { e.dataTransfer.setData('text/plain', row.dataset.id || ''); } catch (_) {}
    });
    row.addEventListener('dragend', async () => {
      row.classList.remove('dragging');
      const ids = [...list.querySelectorAll('.qrow')].map(r => r.dataset.id).filter(Boolean);
      // dragRow clears only after the order is saved: it also gates the poll
      // re-render, which would otherwise snap the list back mid-save.
      try { await api.setOrder(ids, profile); } catch (_) {}
      dragRow = null;
      // A manual reorder implies top-down priority; switch so it takes effect.
      if (cfg && cfg.priorityMode !== 'top_down') {
        cfg.priorityMode = 'top_down';
        api.setPriority('top_down').catch(() => {});
        const sel = document.querySelector('#prio');
        if (sel) sel.value = 'top_down';
      }
    });
  });
  list.addEventListener('dragover', e => {
    e.preventDefault();
    if (!dragRow) return;
    if (!list.contains(dragRow)) { dragRow = null; return; } // re-rendered mid-drag
    const after = queueDragAfter(list, e.clientY);
    if (after == null) list.appendChild(dragRow);
    else list.insertBefore(dragRow, after);
  });
}

async function renderHistory() {
  const state = await api.state();
  const { profiles, profile, multiProfile } = pickProfile(state);
  const prof = profile || { platforms: [], claims: [], stats: {}, spark: [] };
  const multi = isMulti(prof.platforms);
  const claims = prof.claims || [];
  const stats = prof.stats || {};
  const spark = prof.spark || [];
  const v = $('#v-history');
  const hours = ((stats.minutesWatched || 0) / 60).toFixed(1);
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">History &amp; report</h2><p class="sub">Lifetime totals from the local SQLite database${multi ? ', across every platform' : ''}. A session is one continuous watch of a single channel toward a single drop (page refreshes do not end it); it ends on completion or claim, channel loss (offline, raid, stall), a skip or pause, or the max-session rotation (default 4h). Minutes watched is wall-clock time on stream — simultaneous drops credit in parallel, so per-drop progress can outpace it. The session in progress is included live.</p></div>`));
  if (multiProfile) v.append(profilePillBar(profiles, () => renderHistory()));
  v.append(el(`<div class="stat-row">
    <div class="card stat"><div class="big ok">${stats.dropsEarned}</div><div class="cap">Drops earned</div></div>
    <div class="card stat"><div class="big ok">${stats.dropsClaimed}</div><div class="cap">Drops claimed</div></div>
    <div class="card stat"><div class="big">${hours}h</div><div class="cap">Watch time</div></div>
    <div class="card stat"><div class="big">${stats.successRate}%</div><div class="cap">Session success</div></div>
    <div class="card stat"><div class="big">${stats.sessions}</div><div class="cap">Sessions</div></div>
  </div>`));
  const grid = el('<div class="grid2"></div>');
  const claimsCard = el(`<div class="card mini"><h3>Recent claims</h3><div style="margin:0 -16px -15px">${claims.length ? claims.map(c => `
    <div class="clm"><span class="gift">🎁</span><div><div class="n">${multi ? pfBadge(c.platform) + ' ' : ''}${esc(c.dropName)}</div><div class="g">${esc(c.game)}</div></div><div class="when">${esc(fmtDate(c.claimedAt))}</div></div>`).join('') : '<div class="empty">No claims yet.</div>'}</div></div>`);
  const outcomes = stats.outcomeCounts || {};
  const side = el(`<div class="stack">
    <div class="card mini"><h3>Minutes watched · recent</h3><div class="spark">${spark.map(h => `<i style="height:${h}%"></i>`).join('')}</div></div>
    <div class="card mini"><h3>Session outcomes</h3>${Object.keys(outcomes).length ? Object.entries(outcomes).map(([k, v]) => `<div class="kv"><span class="k">${esc(cap(k))}</span><span class="v">${v}</span></div>`).join('') : '<div class="empty">No sessions yet.</div>'}</div>
  </div>`);
  grid.append(claimsCard, side);
  v.append(grid);
}

let settingsPane = 'games';
let lastDebug = null;
// debugProfile scopes the Debugging view; independent of selectedProfile
// because debug data also lists disabled profiles.
let debugProfile = null;

const SETTINGS_PANES = [
  { key: 'games', label: 'Games', icon: '◵', tint: 'var(--ok-dim)', fg: '#4bbd85' },
  { key: 'platforms', label: 'Platforms', icon: '◨', tint: 'var(--twitch-dim)', fg: '#b98cff' },
  { key: 'player', label: 'Player', icon: '▸', tint: 'var(--accent-dim)', fg: '#7cc0ff' },
  { key: 'browser', label: 'Browser', icon: '◱', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
  { key: 'app', label: 'App', icon: '◐', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
  { key: 'profiles', label: 'Profiles', icon: '◈', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
];

const toggleHTML = (id, on) => `<div class="switch ${on ? 'on' : ''}" role="switch" aria-checked="${on}" tabindex="0" data-toggle="${id}"><i></i></div>`;

async function renderSettings() {
  cfg = await loadCfg();
  const v = $('#v-settings');
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">Settings</h2><p class="sub">Saved to config.yaml. Platform and profile changes apply immediately; Chrome launch settings apply the next time that window opens.</p></div>`));
  const state = await api.state();
  const allProfiles = state.profiles || [];
  if (allProfiles.length > 1) {
    const enabled = allProfiles.filter(p => p.enabled);
    if (enabled.length) v.append(profilePillBar(enabled, () => renderSettings()));
  }
  const wrap = el(`<div class="set-wrap"><nav class="set-nav" id="set-nav"></nav><div id="set-content"></div></div>`);
  v.append(wrap);
  const nav = $('#set-nav', wrap);
  nav.innerHTML = SETTINGS_PANES.map(n => `<div class="sn ${n.key === settingsPane ? 'active' : ''}" data-pane="${n.key}"><span class="sq" style="background:${n.tint};color:${n.fg}">${n.icon}</span> ${n.label}</div>`).join('');
  nav.querySelectorAll('[data-pane]').forEach(x => x.addEventListener('click', () => {
    settingsPane = x.dataset.pane;
    nav.querySelectorAll('.sn').forEach(s => s.classList.toggle('active', s.dataset.pane === settingsPane));
    renderSettingsContent();
  }));
  renderSettingsContent();
}

function settingsPaneHTML() {
  switch (settingsPane) {
    case 'platforms': return `
      <div class="group"><div class="caption">Twitch</div><div class="card">
        <div class="row"><div class="lab"><div class="l">${pfBadge('twitch')} Twitch drops</div><div class="h">The primary platform — always on. The claim behavior below applies to Twitch only.</div></div><div class="ctl"><span class="pill active"><span class="d"></span> Always on</span></div></div>
        <div class="row"><div class="lab"><div class="l">Skip account-link drops</div><div class="h">Skip campaigns Twitch reports as unlinked. Drops the server is already crediting are watched regardless, and Off watches everything (failed claims surface for manual reconcile).</div></div><div class="ctl">${toggleHTML('skipAccountLink', cfg.skipAccountLink)}</div></div>
        <div class="row"><div class="lab"><div class="l">Allow GQL claim fallback</div><div class="h">Off by design — replaying the claim mutation has triggered bot blocks.</div></div><div class="ctl">${toggleHTML('allowGqlFallback', cfg.allowGqlFallback)}</div></div>
      </div></div>
      <div class="group"><div class="caption">Kick</div><div class="card">
        <div class="row"><div class="lab"><div class="l">${pfBadge('kick')} Kick drops</div><div class="h">Runs a second dedicated Chrome and watcher engine in parallel with Twitch, sharing the game filter and queue. Starts and stops immediately.</div></div><div class="ctl">${toggleHTML('kickEnabled', cfg.kickEnabled)}</div></div>
        <div class="row"><div class="lab"><div class="l">First run</div><div class="h">Log in to Kick once in its own Chrome window; the profile keeps the session. Kick account links are only needed at claim time, so unlinked campaigns are still watched. Kick rewards expire if left unclaimed — watch the needs-attention banner.</div></div></div>
      </div></div>`;
    case 'player': return `
      <div class="group"><div class="caption">Player</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Watch volume</div><div class="h">Kept low, never muted — a muted background tab stops crediting on Chrome 145+. Applies to every platform's watcher.</div></div>
          <div class="ctl" style="display:flex;align-items:center;gap:10px"><input class="input" type="number" min="0.005" max="1" step="0.005" value="${cfg.volume}" data-field="volume" style="width:96px"><span class="mono" id="vol-pct" style="color:var(--text-secondary);min-width:36px;text-align:right">${Math.round(cfg.volume * 100)}%</span></div></div>
        <div class="row"><div class="lab"><div class="l">Force lowest quality</div><div class="h">Saves bandwidth and CPU; does not affect drop credit.</div></div><div class="ctl">${toggleHTML('forceLowestQuality', cfg.forceLowestQuality)}</div></div>
        <div class="row"><div class="lab"><div class="l">Mute Chrome audio</div><div class="h">Silences the watcher Chromes at the sink while the player stays unmuted at 1%, so crediting is unaffected. Needs a Chrome restart.</div></div><div class="ctl">${toggleHTML('muteAudio', cfg.muteAudio)}</div></div>
        <div class="row"><div class="lab"><div class="l">Keep Chrome minimized</div><div class="h">Minimizes only the watcher windows (via CDP). Each restores itself briefly while a channel is opened and validated — a hidden player will not autoplay — then re-minimizes once watching. Needs a Chrome restart.</div></div><div class="ctl">${toggleHTML('startMinimized', cfg.startMinimized)}</div></div>
      </div></div>`;
    case 'browser': return `
      <div class="group"><div class="caption">Browser</div><div class="card">
        <div class="row stacked"><div class="lab"><div class="l">Chrome path</div><div class="h">Blank = auto-detect. Override for Chrome Beta or an unusual install. Both platforms' watcher instances use this binary with separate profiles.</div></div>
          <div class="ctl"><input class="input mono" data-field="chromePath" value="${esc(cfg.chromePath)}" placeholder="auto-detected" style="width:100%"></div></div>
      </div></div>`;
    case 'app': return `
      <div class="group"><div class="caption">Appearance</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Theme</div><div class="h">System follows the OS light/dark preference.</div></div><div class="ctl"><select class="popup" data-field="theme"><option value="dark">Dark</option><option value="light">Light</option><option value="system">System</option></select></div></div>
      </div></div>
      <div class="group"><div class="caption">Account</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Auth mode</div><div class="h">Manual login is safest.</div></div><div class="ctl"><select class="popup" data-field="authMode"><option value="manual">Manual</option><option value="stored" disabled>Stored (unsupported)</option></select></div></div>
      </div>
      <div class="warnnote">⚠ Stored credentials are disabled by design and unsupported. Leave off unless manual login stops working.</div></div>`;
    default: return `
      <div class="group"><div class="caption">Games</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Any game</div><div class="h">Queue drops for every game, ignoring the list below. Good for a quick test.</div></div><div class="ctl">${toggleHTML('anyGame', cfg.anyGame)}</div></div>
        <div class="row stacked"><div class="lab"><div class="l">Tracked games</div><div class="h">Only campaigns for these games are queued, on every platform.</div></div>
          <div class="ctl" id="games-ctl"></div></div>
        <div class="row"><div class="lab"><div class="l">Auto-enqueue new campaigns</div><div class="h">Add newly discovered drops to the bottom of the queue.</div></div><div class="ctl">${toggleHTML('autoEnqueueNew', cfg.autoEnqueueNew)}</div></div>
        <div class="row"><div class="lab"><div class="l">Fall through to next available</div><div class="h">If the top drop has no live channel, watch the next available one and return to the top when its channel is live. Off = wait on the top drop.</div></div><div class="ctl">${toggleHTML('fallThrough', cfg.fallThrough)}</div></div>
      </div></div>`;
  }
}

function renderSettingsContent() {
  const host = $('#set-content');
  if (!host) return;
  // The Profiles pane edits its own data (per-profile overlay, not the shared
  // cfg clone) through its own endpoints, so it skips the generic
  // settingsPaneHTML()+Save/Discard wiring below entirely.
  if (settingsPane === 'profiles') {
    host.innerHTML = '<div class="empty">Loading profiles...</div>';
    Promise.all([loadProfileEdits(), loadProfileLive()]).then(() => {
      if (settingsPane === 'profiles') renderProfilesPane(host);
    });
    return;
  }
  host.innerHTML = settingsPaneHTML() +
    `<div class="foot-btns"><button class="btn primary" id="save">Save changes</button><button class="btn" id="discard">Discard changes</button><span id="savemsg" class="save-msg"></span></div>`;

  host.querySelectorAll('[data-toggle]').forEach(sw => {
    const flip = () => {
      const on = !sw.classList.contains('on');
      sw.classList.toggle('on', on);
      sw.setAttribute('aria-checked', String(on));
      cfg[sw.dataset.toggle] = on;
      if (sw.dataset.toggle === 'anyGame') renderGamesCtl();
    };
    sw.addEventListener('click', flip);
    sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); } });
  });
  host.querySelectorAll('[data-field]').forEach(inp => inp.addEventListener('input', () => {
    const f = inp.dataset.field;
    if (f === 'volume') {
      const n = parseFloat(inp.value);
      if (Number.isFinite(n)) cfg.volume = n; // keep last good value if the field is cleared
      const p = $('#vol-pct', host);
      if (p) p.textContent = Math.round((Number.isFinite(n) ? n : cfg.volume) * 100) + '%';
    } else {
      cfg[f] = inp.value;
    }
  }));
  const authSel = host.querySelector('select[data-field="authMode"]');
  if (authSel) authSel.value = cfg.authMode;
  const themeSel = host.querySelector('select[data-field="theme"]');
  if (themeSel) themeSel.value = cfg.theme || 'dark';
  if (settingsPane === 'games') renderGamesCtl();

  $('#save', host).addEventListener('click', saveSettings);
  $('#discard', host).addEventListener('click', () => renderSettings());
}

function renderGamesCtl() {
  const gamesCtl = document.querySelector('#games-ctl');
  if (!gamesCtl) return;
  const disabled = cfg.anyGame;
  gamesCtl.style.opacity = disabled ? '0.45' : '1';
  gamesCtl.style.pointerEvents = disabled ? 'none' : 'auto';
  const chips = (cfg.games || []).map((g, i) => `<span class="chip">${esc(g)} <span class="x" data-rm="${i}" style="cursor:pointer" title="remove">×</span></span>`).join('');
  gamesCtl.innerHTML = `<div class="chips" style="justify-content:flex-start">${chips || '<span style="color:var(--text-tertiary);font-size:12px">no games — add one or turn on Any game</span>'}</div>
    <div style="display:flex;gap:8px;margin-top:8px"><input class="input" id="game-add" placeholder="Exact game name, e.g. Special Events" style="flex:1"><button class="btn" id="game-add-btn" type="button">Add</button></div>`;
  gamesCtl.querySelectorAll('[data-rm]').forEach(x => x.addEventListener('click', () => { cfg.games.splice(+x.dataset.rm, 1); renderGamesCtl(); }));
  const addInput = gamesCtl.querySelector('#game-add');
  const addGame = () => { const val = addInput.value.trim(); if (val && !cfg.games.includes(val)) cfg.games.push(val); renderGamesCtl(); if (document.querySelector('#game-add')) document.querySelector('#game-add').focus(); };
  gamesCtl.querySelector('#game-add-btn').addEventListener('click', addGame);
  addInput.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addGame(); } });
}

// ---- Settings > Profiles pane ----

// profileEdits is a mutable CLONE of GET /api/profiles (the loadCfg pattern,
// applied to profile overlays instead of the shared config). Every toggle here
// is live: saving one starts or stops that profile's runtimes, so there is no
// pending-restart state to track. profileSnapshot is the UNTOUCHED copy taken
// at the same time, kept so saveProfileEdits can tell which profiles actually
// changed instead of re-sending every toggle for every profile.
let profileEdits = null;
let profileSnapshot = null;
// profileLive maps profile name -> its live platforms array (from /api/state),
// used only to render runtime status dots in the Profiles pane - separate from
// profileEdits, which holds the editable overlay, not runtime state.
let profileLive = {};

async function loadProfileEdits() {
  const list = await api.profileConfigs();
  profileEdits = (list || []).map(p => ({ ...p, games: [...(p.games || [])] }));
  profileSnapshot = (list || []).map(p => ({ ...p, games: [...(p.games || [])] }));
}

async function loadProfileLive() {
  const state = await api.state();
  const live = {};
  (state.profiles || []).forEach(p => { live[p.name] = p.platforms || []; });
  profileLive = live;
}

function arraysEqual(a, b) {
  a = a || []; b = b || [];
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

// platformRuntimeStatus resolves one platform's live status for the Profiles
// pane row, reusing the footer's enabled/up semantics (footRow above): a
// disabled platform reads "stopped"; enabled but down reads "needs login" when
// it has never signed in and "stopped" otherwise (Chrome launches only while a
// watcher runs); an up window parked on the login page reads "logging in".
function platformRuntimeStatus(p) {
  if (!p || !p.enabled) return { cls: 'off', label: 'stopped' };
  if (!p.up) {
    if (p.wanted) return { cls: 'warn', label: 'launching' };
    return p.needsLogin ? { cls: 'warn', label: 'needs login' } : { cls: 'off', label: 'stopped' };
  }
  if (p.needsLogin) return { cls: 'warn', label: 'logging in' };
  return { cls: 'ok', label: 'running' };
}

function renderProfilesPane(host) {
  host.innerHTML = `
    <div class="group"><div class="caption">Profiles</div><div class="card" id="prof-list"></div></div>
    <div class="group"><div class="caption">Add profile</div><div class="card">
      <div class="row stacked">
        <div class="lab"><div class="l">New profile</div><div class="h">Name is a lowercase slug: letters, digits, hyphens, 1-24 characters. The profile starts immediately; log in to Twitch (and Kick if enabled) in its watcher window.</div></div>
        <div class="ctl" style="display:flex;gap:8px;flex-wrap:wrap">
          <input class="input" id="np-name" placeholder="slug, e.g. profile-two" style="flex:1;min-width:140px">
          <input class="input" id="np-display" placeholder="Display name (optional)" style="flex:1;min-width:140px">
          <button class="btn" id="np-add" type="button">Add profile</button>
        </div>
      </div>
      <div class="row"><span id="np-msg" class="save-msg"></span></div>
    </div></div>
    <div class="foot-btns"><button class="btn primary" id="prof-save">Save changes</button><button class="btn" id="prof-discard">Discard changes</button><span id="prof-msg" class="save-msg"></span></div>`;
  renderProfileRows(host);
  host.querySelector('#prof-save').addEventListener('click', () => saveProfileEdits(host));
  host.querySelector('#prof-discard').addEventListener('click', () => renderSettingsContent());
  host.querySelector('#np-add').addEventListener('click', () => addProfileFlow(host));
}

function renderProfileRows(host) {
  const list = host.querySelector('#prof-list');
  if (!list) return;
  list.innerHTML = '';
  const onlyOne = (profileEdits || []).length <= 1;
  (profileEdits || []).forEach(p => {
    const livePlats = profileLive[p.name] || [];
    const twitchLive = livePlats[0] || { enabled: p.enabled, up: false, needsLogin: false };
    const kickLive = livePlats[1] || { enabled: false, up: false, needsLogin: false };
    const twStatus = platformRuntimeStatus(twitchLive);
    const kStatus = platformRuntimeStatus(kickLive);
    const showKick = kickLive.enabled || kickLive.up;
    const row = el(`<div class="row stacked">
      <div style="display:flex;align-items:center;gap:14px;flex-wrap:wrap">
        <input class="input" data-pf="displayName" value="${esc(p.displayName)}" placeholder="${esc(p.name)}" style="width:160px">
        <span class="mono" style="color:var(--text-tertiary);font-size:11.5px">${esc(p.name)}</span>
        <div style="flex:1"></div>
        <label style="display:flex;align-items:center;gap:8px;font-size:12.5px;color:var(--text-secondary)">${pfBadge('kick')} ${toggleHTML('kickEnabled', p.kickEnabled)}</label>
        <label style="display:flex;align-items:center;gap:8px;font-size:12.5px;color:var(--text-secondary)">Enabled ${toggleHTML('enabled', p.enabled)}</label>
      </div>
      <div style="display:flex;gap:16px;flex-wrap:wrap;font-size:11.5px;color:var(--text-secondary)">
        <span style="display:flex;align-items:center;gap:6px"><span class="fdot ${twStatus.cls}"></span>Twitch ${esc(twStatus.label)}</span>
        ${showKick ? `<span style="display:flex;align-items:center;gap:6px"><span class="fdot ${kStatus.cls}"></span>Kick ${esc(kStatus.label)}</span>` : ''}
      </div>
      <label style="display:flex;align-items:center;gap:8px;font-size:12.5px;color:var(--text-secondary)">Inherit shared games list ${toggleHTML('inheritGames', p.inheritGames)}</label>
      <div class="games-ctl" data-pf-games style="${p.inheritGames ? 'opacity:.45;pointer-events:none' : ''}"></div>
      <div data-pf-remove style="display:flex;justify-content:flex-end;align-items:center;gap:8px"></div>
    </div>`);
    row.querySelectorAll('[data-toggle]').forEach(sw => {
      sw.addEventListener('click', () => {
        const on = !sw.classList.contains('on');
        sw.classList.toggle('on', on);
        sw.setAttribute('aria-checked', String(on));
        p[sw.dataset.toggle] = on;
        if (sw.dataset.toggle === 'inheritGames') {
          const gc = row.querySelector('[data-pf-games]');
          if (gc) { gc.style.opacity = on ? '.45' : '1'; gc.style.pointerEvents = on ? 'none' : 'auto'; }
        }
      });
      sw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); sw.click(); } });
    });
    row.querySelector('[data-pf="displayName"]').addEventListener('input', e => { p.displayName = e.target.value; });
    renderProfileGamesChips(row.querySelector('[data-pf-games]'), p);
    attachRemoveControl(row.querySelector('[data-pf-remove]'), p, onlyOne, host);
    list.append(row);
  });
}

// attachRemoveControl builds the Remove button and its inline two-step confirm
// (no window.confirm): a first click flips the button to "Really remove
// <name>?" and adds a Cancel button; a second click on the flipped button
// calls the remove API. Disabled (with an explanatory title) when this is the
// only profile - the backend refuses that with a 400 anyway, but greying it
// out beats accept-then-error.
function attachRemoveControl(wrap, p, onlyOne, host) {
  if (!wrap) return;
  const btn = el(`<button class="btn ghost danger" type="button" ${onlyOne ? 'disabled title="Cannot remove the only profile - add another one first"' : ''}>Remove profile</button>`);
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
      const msg = host.querySelector('#prof-msg');
      if (msg) { msg.textContent = `Profile removed. Its queue, history and logins are still on disk at ${r.dataDir}.`; msg.style.color = 'var(--ok)'; }
      await Promise.all([loadProfileEdits(), loadProfileLive()]);
      if (settingsPane === 'profiles') renderProfileRows(host);
    } catch (e) {
      reset();
      const msg = host.querySelector('#prof-msg');
      if (msg) { msg.textContent = 'Error: ' + (e.message || e); msg.style.color = 'var(--err)'; }
    }
  });
  wrap.append(btn);
}

// renderProfileGamesChips mirrors renderGamesCtl's chip editor, bound to one
// profile's games override instead of the shared cfg.games.
function renderProfileGamesChips(container, p) {
  if (!container) return;
  const chips = (p.games || []).map((g, i) => `<span class="chip">${esc(g)} <span class="x" data-rm="${i}" style="cursor:pointer" title="remove">×</span></span>`).join('');
  container.innerHTML = `<div class="chips" style="justify-content:flex-start">${chips || '<span style="color:var(--text-tertiary);font-size:12px">no games override - add one or turn inherit back on</span>'}</div>
    <div style="display:flex;gap:8px;margin-top:8px"><input class="input" placeholder="Exact game name" style="flex:1"><button class="btn" type="button">Add</button></div>`;
  container.querySelectorAll('[data-rm]').forEach(x => x.addEventListener('click', () => { p.games.splice(+x.dataset.rm, 1); renderProfileGamesChips(container, p); }));
  const input = container.querySelector('.input');
  const btn = container.querySelector('button');
  const add = () => { const val = input.value.trim(); if (val && !p.games.includes(val)) p.games.push(val); renderProfileGamesChips(container, p); };
  btn.addEventListener('click', add);
  input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); add(); } });
}

// saveProfileEdits sends a request only for a profile field that actually
// changed vs profileSnapshot (the untouched copy loaded alongside profileEdits)
// - every save now triggers a live reconcile, so firing every toggle for every
// profile would restart runtimes that never changed.
async function saveProfileEdits(host) {
  const msg = host.querySelector('#prof-msg');
  try {
    for (const p of profileEdits) {
      const orig = (profileSnapshot || []).find(o => o.name === p.name);
      if (!orig) continue;
      if (orig.enabled !== p.enabled) {
        await api.setProfileEnabled(p.name, p.enabled);
      }
      const overlayChanged = orig.displayName !== p.displayName || orig.kickEnabled !== p.kickEnabled ||
        orig.inheritGames !== p.inheritGames || !arraysEqual(orig.games, p.games);
      if (overlayChanged) {
        await api.updateProfile({ name: p.name, displayName: p.displayName, kickEnabled: p.kickEnabled, games: p.games, inheritGames: p.inheritGames });
      }
    }
    if (msg) { msg.textContent = 'Saved.'; msg.style.color = 'var(--ok)'; }
    await Promise.all([loadProfileEdits(), loadProfileLive()]);
    if (settingsPane === 'profiles') renderProfileRows(host);
    refreshAll();
  } catch (e) {
    if (msg) { msg.textContent = 'Error: ' + (e.message || e); msg.style.color = 'var(--err)'; }
  }
  setTimeout(() => { const m = host.querySelector('#prof-msg'); if (m) m.textContent = ''; }, 4000);
}

async function addProfileFlow(host) {
  const nameInp = host.querySelector('#np-name');
  const dispInp = host.querySelector('#np-display');
  const msg = host.querySelector('#np-msg');
  const name = (nameInp.value || '').trim().toLowerCase();
  if (!/^[a-z0-9-]{1,24}$/.test(name)) {
    if (msg) { msg.textContent = 'Name must be lowercase letters, digits, hyphens (1-24 chars).'; msg.style.color = 'var(--err)'; }
    return;
  }
  try {
    await api.addProfile(name, dispInp.value.trim());
    if (msg) {
      msg.textContent = "Profile added. Chrome only opens on demand now: press Log in on its dashboard card to open the login window, sign in, then Start.";
      msg.style.color = 'var(--ok)';
    }
    nameInp.value = ''; dispInp.value = '';
    await loadProfileEdits();
    if (settingsPane === 'profiles') renderProfileRows(host);
    // Poll briefly so the new row's live status (starting -> running/needs
    // login) appears without the user having to leave and reopen the pane.
    for (let i = 0; i < 4 && settingsPane === 'profiles'; i++) {
      await new Promise(r => setTimeout(r, 1000));
      await loadProfileLive();
      if (settingsPane === 'profiles') renderProfileRows(host);
    }
  } catch (e) {
    if (msg) { msg.textContent = 'Error: ' + (e.message || e); msg.style.color = 'var(--err)'; }
  }
}

async function saveSettings() {
  const msg = document.querySelector('#savemsg');
  try {
    await api.saveConfig(cfg);
    applyTheme(cfg.theme);
    if (msg) { msg.textContent = 'Saved. Refreshing campaigns…'; msg.style.color = 'var(--ok)'; }
    refreshDiscovery();
    setTimeout(() => refreshAll(), 500);
  } catch (e) {
    if (msg) { msg.textContent = 'Error: ' + (e.message || e); msg.style.color = 'var(--err)'; }
  }
  setTimeout(() => { const m = document.querySelector('#savemsg'); if (m) m.textContent = ''; }, 4000);
}

// refreshDiscovery kicks a discovery pass on every running, unpaused engine
// across every profile (a paused engine ignores the request) and returns how
// many were asked.
async function refreshDiscovery() {
  let n = 0;
  try {
    const profiles = await api.profiles();
    for (const prof of profiles) {
      for (const p of (prof.platforms || [])) {
        if (p.up && !(p.status && p.status.paused)) {
          api.engine(p.platform, prof.name).refresh().catch(() => {});
          n++;
        }
      }
    }
  } catch (_) {}
  return n;
}

// ---- Debugging view (own left-nav item) ----

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

function flashMsg(id, text, color) {
  const m = document.querySelector(id);
  if (!m) return;
  m.textContent = text; m.style.color = color || 'var(--ok)';
  setTimeout(() => { const x = document.querySelector(id); if (x) x.textContent = ''; }, 2500);
}

async function renderDebug() {
  const v = $('#v-debug');
  if (!v) return;
  // Build the static shell once; the panel refreshes in place.
  if (!v.querySelector('#debug-panel')) {
    v.innerHTML = '';
    v.append(el(`<div><h2 class="title">Debugging</h2><p class="sub">Live runtime state and diagnostics per platform. The exported log and JSON contain campaign data only — no auth tokens.</p></div>`));
    const controls = el(`<div class="foot-btns" style="flex-wrap:wrap;margin-bottom:14px">
      <button class="btn" id="dbg-refresh">Refresh</button>
      <button class="btn" id="dbg-discover">Force discovery</button>
      <button class="btn" id="dbg-copy-log">Copy log</button>
      <button class="btn" id="dbg-copy-json">Copy all (JSON)</button>
      <label class="dbg-inline"><span>Dump GraphQL bodies</span>${toggleHTML('captureGqlBodies', !!(cfg && cfg.captureGqlBodies))}</label>
      <span id="dbg-msg" class="save-msg"></span>
    </div>`);
    v.append(controls);
    v.append(el(`<div id="debug-panel"></div>`));

    controls.querySelector('#dbg-refresh').addEventListener('click', renderDebugPanel);
    controls.querySelector('#dbg-discover').addEventListener('click', async () => {
      const n = await refreshDiscovery();
      if (n > 0) { flashMsg('#dbg-msg', `discovery requested (${n})`); setTimeout(renderDebugPanel, 1000); }
      else flashMsg('#dbg-msg', 'no running engine to refresh — resume first', 'var(--warn)');
    });
    controls.querySelector('#dbg-copy-log').addEventListener('click', async () => {
      const ok = await copyText(((lastDebug && lastDebug.logTail) || []).join('\n'));
      flashMsg('#dbg-msg', ok ? 'log copied' : 'copy failed', ok ? 'var(--ok)' : 'var(--err)');
    });
    controls.querySelector('#dbg-copy-json').addEventListener('click', async () => {
      const ok = await copyText(JSON.stringify(lastDebug || {}, null, 2));
      flashMsg('#dbg-msg', ok ? 'JSON copied' : 'copy failed', ok ? 'var(--ok)' : 'var(--err)');
    });
    const dumpSw = controls.querySelector('[data-toggle="captureGqlBodies"]');
    if (dumpSw) {
      const flipDump = async () => {
        const on = !dumpSw.classList.contains('on');
        dumpSw.classList.toggle('on', on);
        dumpSw.setAttribute('aria-checked', String(on));
        // Save on a fresh copy of the server config so this never commits
        // unsaved edits sitting in the Settings form.
        try {
          await api.refreshState();
          const c = await loadCfg();
          c.captureGqlBodies = on;
          await api.saveConfig(c);
          if (cfg) cfg.captureGqlBodies = on;
          flashMsg('#dbg-msg', on ? 'dumping enabled' : 'dumping disabled');
        } catch (e) { flashMsg('#dbg-msg', 'save failed', 'var(--err)'); }
      };
      dumpSw.addEventListener('click', flipDump);
      dumpSw.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flipDump(); } });
    }
  }
  // Keep the dump toggle in sync with config (it was built once with the shell).
  if (!cfg) cfg = await loadCfg().catch(() => null);
  const dumpSw = document.querySelector('[data-toggle="captureGqlBodies"]');
  if (dumpSw && cfg) { dumpSw.classList.toggle('on', !!cfg.captureGqlBodies); dumpSw.setAttribute('aria-checked', String(!!cfg.captureGqlBodies)); }
  await renderDebugPanel();
}

// debugPlatformCard renders one platform's runtime/eligibility/discovery card.
function debugPlatformCard(name, p) {
  const st = p.status || {};
  const elig = p.eligibility || {};
  const eligStr = Object.keys(elig).length ? Object.entries(elig).map(([k, v]) => `${cap(k)} ${v}`).join('   ') : '—';
  const runLine = p.up
    ? `up · ${esc(st.state || '—')}${st.paused ? ' (paused)' : ''}${st.channelLogin ? ' @' + esc(st.channelLogin) : ''}`
    : (p.enabled ? 'not running' : 'disabled');
  const disc = p.lastDiscovery;
  let discLine = 'not run yet';
  let extra = '';
  if (name === 'twitch') {
    if (disc) discLine = `gql ${disc.gqlResponses} · parsed ${disc.campaignsParsed} · details ${disc.detailsParsed} · enqueued ${disc.enqueued}`;
    const integrity = ((disc && disc.headerKeys) || []).indexOf('client-integrity') !== -1;
    extra = `<div class="kv"><span class="k">Integrity token captured</span><span class="v" style="color:${integrity ? 'var(--ok)' : 'var(--err)'}">${integrity ? 'yes' : 'no'}</span></div>`;
  } else if (disc) {
    discLine = `api ${disc.apiStatus || '—'} · parsed ${disc.campaignsParsed} · enqueued ${disc.enqueued}`;
    if (disc.lastError) extra = `<div class="kv"><span class="k">Last error</span><span class="v" style="color:var(--err)">${esc(disc.lastError)}</span></div>`;
  }
  return `<div class="card mini" style="margin-bottom:14px">
    <h3>${pfBadge(name)} ${esc(platLabel(name))}</h3>
    <div class="kv"><span class="k">Runtime</span><span class="v">${runLine}</span></div>
    <div class="kv"><span class="k">Eligibility</span><span class="v mono">${esc(eligStr)}</span></div>
    <div class="kv"><span class="k">Last discovery</span><span class="v mono">${esc(discLine)}</span></div>
    ${extra}
  </div>`;
}

async function renderDebugPanel() {
  const host = document.querySelector('#debug-panel');
  if (!host) return;
  let d;
  try { d = await api.debug(); lastDebug = d; }
  catch (e) { host.innerHTML = '<div class="card mini"><div class="empty">Debug info needs the backend running (not a static preview).</div></div>'; return; }
  // One profile's sections at a time, with its own pill bar (this view sits
  // outside the shared Queue/History/Settings selector).
  const names = Object.keys(d.profiles || {});
  if (!debugProfile || !names.includes(debugProfile)) debugProfile = names[0] || '';
  const name = debugProfile;
  const prof = (d.profiles || {})[name] || {};
  const paths = ((d.paths || {}).profiles || {})[name] || {};
  const logText = (d.logTail || []).slice(-120).join('\n') || 'no log lines yet';
  let pills = '';
  if (names.length > 1) {
    pills = `<div class="prof-pills">` + names.map(n => {
      const dn = ((d.profiles || {})[n] || {}).displayName || n;
      return `<button type="button" class="prof-pill ${n === name ? 'active' : ''}" data-dbgprof="${esc(n)}">${esc(dn)}</button>`;
    }).join('') + `</div>`;
  }
  let cards = debugPlatformCard('twitch', prof.twitch || {});
  if (prof.kick && prof.kick.enabled) cards += debugPlatformCard('kick', prof.kick);
  host.innerHTML = pills + cards + `<div class="card mini">
    <div class="kv"><span class="k">Debug folder</span><span class="v mono" style="font-size:11px;max-width:62%;overflow:hidden;text-overflow:ellipsis" title="${esc(paths.debugDir || '')}">${esc(paths.debugDir || '')}</span></div>
    <div class="log-head"><div class="caption" style="margin:0">Log tail</div><button class="btn ghost btn-sm" id="dbg-log-copy" type="button">Copy</button></div>
    <pre id="dbg-log" class="log-pre">${esc(logText)}</pre>
  </div>`;
  host.querySelectorAll('[data-dbgprof]').forEach(b => b.addEventListener('click', () => {
    debugProfile = b.dataset.dbgprof;
    renderDebugPanel();
  }));
  const logEl = host.querySelector('#dbg-log');
  if (logEl) logEl.scrollTop = logEl.scrollHeight;
  const copyBtn = host.querySelector('#dbg-log-copy');
  if (copyBtn) copyBtn.addEventListener('click', async () => {
    const ok = await copyText(logText);
    copyBtn.textContent = ok ? 'Copied' : 'Failed';
    setTimeout(() => { if (copyBtn) copyBtn.textContent = 'Copy'; }, 1500);
  });
}

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

// go switches to a view and renders it on demand.
function go(view) {
  switchView(view);
  if (view === 'queue') renderQueue();
  else if (view === 'history') renderHistory();
  else if (view === 'settings') renderSettings();
  else if (view === 'debug') renderDebug();
  else renderDashboard();
}

// queueBusy: skip the queue poll re-render while the user is mid-drag or has
// the priority dropdown open (a re-render would yank it shut under the cursor).
function queueBusy() {
  if (dragRow) return true;
  const a = document.activeElement;
  return !!(a && a.tagName === 'SELECT' && a.closest('#v-queue'));
}

function init() {
  document.querySelectorAll('.nav').forEach(n => {
    n.addEventListener('click', () => go(n.dataset.view));
    n.addEventListener('keydown', e => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); go(n.dataset.view); } });
  });
  // Load config first so cfg is available (theme), then render everything.
  loadCfg().then(c => {
    cfg = c;
    applyTheme(cfg && cfg.theme);
    refreshAll();
  }).catch(() => refreshAll());
  // Live updates: re-poll the active view periodically (api.onEvent is a no-op
  // without pushed events, so polling is the live-update path).
  setInterval(() => {
    if (isActive('dashboard')) renderDashboard();
    else if (isActive('queue') && !queueBusy()) renderQueue();
    else if (isActive('history')) renderHistory();
    renderFoot();
  }, 5000);
  // Auto-refresh the debug panel while the Debugging view is active.
  setInterval(() => { if (isActive('debug')) renderDebugPanel(); }, 4000);
  api.onEvent('watcher:progress', () => renderDashboard());
  api.onEvent('watcher:state', () => { renderDashboard(); renderFoot(); });
  api.onEvent('drop:claimed', () => refreshAll());
}

function isActive(view) {
  const s = document.querySelector('.view[data-view="' + view + '"]');
  return s && s.classList.contains('active');
}

init();
