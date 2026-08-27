import { api } from '/api.js';

const $ = (s, r = document) => r.querySelector(s);
const el = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const pct = (cur, req) => req > 0 ? Math.min(100, Math.round(cur / req * 100)) : 0;

let cfg = null;

function switchView(view) {
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.dataset.view === view));
  document.querySelectorAll('.nav').forEach(n => n.classList.toggle('active', n.dataset.view === view));
}

async function renderFoot() {
  const c = await api.connection();
  const foot = $('#rail-foot');
  if (api.offline) {
    foot.innerHTML = `<span class="dot off">●</span> preview mode<br>backend not connected`;
  } else if (c.chrome) {
    foot.innerHTML = `<span class="dot">●</span> Chrome connected<br>watcher running`;
  } else {
    foot.innerHTML = `<span class="dot off">●</span> starting Chrome…`;
  }
}

async function renderDashboard() {
  const st = await api.status();
  const queue = await api.queue();
  const stats = await api.stats();
  const upnext = queue.filter(q => q.status === 'pending').slice(0, 3);
  const watching = st.state === 'watching';

  const v = $('#v-dashboard');
  v.innerHTML = '';
  v.append(el(`<div>
    <h2 class="title">Now watching</h2>
    <p class="sub">One channel at a time. The watcher keeps this window unfocused and dimmed while progress ticks.</p>
  </div>`));

  const grid = el(`<div class="grid2"></div>`);
  const nowCard = el(`<div class="card now">
    <div class="head">
      <div class="avatar">${esc((st.channelLogin || '--').slice(0, 2).toUpperCase())}</div>
      <div class="who">
        <div class="ch">${esc(st.channelLogin || 'idle')} ${watching ? '<span class="live">LIVE</span>' : ''}</div>
        <div class="game">${esc(st.game || '')}${st.viewers ? ' · ' + st.viewers.toLocaleString() + ' viewers' : ''}</div>
      </div>
      <span class="pill ${esc(st.state)}"><span class="d"></span> ${esc(cap(st.state))}</span>
    </div>
    ${watching ? `<div class="drop-row">
      <div class="lbl"><span class="name">${esc(st.dropName)}</span><span class="mins mono">${st.current} / ${st.required} min</span></div>
      <div class="bar"><i style="width:${pct(st.current, st.required)}%"></i></div>
    </div>
    <div class="watchdogs">
      <span class="wd"><span class="tick">✓</span> Progress ticking</span>
      <span class="wd"><span class="tick">✓</span> Playback healthy</span>
      <span class="wd"><span class="tick">✓</span> Volume 1%</span>
    </div>` : `<div class="empty">Not watching. Press Start to begin working the queue.</div>`}
    <div class="foot-btns">
      ${watching ? '<button class="btn" data-act="skip">Skip drop</button>' : '<button class="btn primary" data-act="start">Start</button>'}
      <button class="btn ghost" data-act="refresh">Refresh campaigns</button>
      ${watching ? '<button class="btn ghost danger" data-act="stop">Pause</button>' : ''}
    </div>
  </div>`);

  const side = el(`<div class="stack">
    <div class="card mini"><h3>Up next</h3><div class="upnext">${upnext.length ? upnext.map((q, i) => `
      <div class="up"><span class="idx">${i + 1}</span><div class="t"><div class="n">${esc(q.dropName)}</div><div class="g">${esc(q.game)} · ${esc(q.reason || '')}</div></div><span class="m mono">${q.currentMinutes}/${q.requiredMinutes}</span></div>`).join('') : '<div class="empty">Queue is empty.</div>'}</div></div>
    <div class="card mini"><h3>Totals</h3>
      <div class="kv"><span class="k">Drops claimed</span><span class="v">${stats.dropsClaimed}</span></div>
      <div class="kv"><span class="k">Minutes watched</span><span class="v">${stats.minutesWatched}</span></div>
      <div class="kv"><span class="k">Sessions</span><span class="v">${stats.sessions}</span></div>
      <div class="kv"><span class="k">Success rate</span><span class="v">${stats.successRate}%</span></div>
    </div>
  </div>`);

  grid.append(nowCard, side);
  v.append(grid);

  v.querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', async () => {
    const act = b.dataset.act;
    if (act === 'start') await api.start();
    if (act === 'stop') await api.stop();
    if (act === 'skip') await api.skip();
    if (act === 'refresh') await api.refresh();
    refreshAll();
  }));
}

async function renderQueue() {
  const queue = await api.queue();
  const v = $('#v-queue');
  v.innerHTML = '';
  v.append(el(`<div class="qhead">
    <div><h2 class="title">Queue</h2><p class="sub" style="margin:0">Drag to reorder. The watcher works top to bottom.</p></div>
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
  if (cfg) $('#prio', v).value = cfg.priorityMode;
  $('#prio', v).addEventListener('change', async e => { await api.setPriority(e.target.value); });

  const list = el(`<div class="card qlist"></div>`);
  if (!queue.length) list.append(el('<div class="empty">No drops queued yet. Run a discovery pass.</div>'));
  for (const q of queue) {
    const row = el(`<div class="qrow ${q.status === 'active' ? 'active' : ''}" data-id="${esc(q.dropID)}" draggable="true">
      <span class="grip" title="drag to reorder">⁝⁝</span>
      <div class="qname"><div class="n">${esc(q.dropName || q.dropID)}</div><div class="meta">${esc(q.game)}${q.reason ? ' · ' + esc(q.reason) : ''}</div></div>
      <div class="qmini"><div class="bar"><i class="${q.status === 'active' ? '' : 'blue'}" style="width:${pct(q.currentMinutes, q.requiredMinutes)}%"></i></div><div class="mm mono">${q.requiredMinutes ? q.currentMinutes + ' / ' + q.requiredMinutes + ' min' : '—'}</div></div>
      <span class="pill ${esc(q.status)}"><span class="d"></span> ${esc(cap(q.status))}</span>
      <div class="switch ${q.enabled ? 'on' : ''}" role="switch" aria-checked="${q.enabled}" tabindex="0"><i></i></div>
    </div>`);
    row.querySelector('.switch').addEventListener('click', async (e) => {
      const on = !e.currentTarget.classList.contains('on');
      e.currentTarget.classList.toggle('on', on);
      await api.setEnabled(q.dropID, on);
    });
    list.append(row);
  }
  v.append(list);
  if (queue.length > 1) attachQueueDrag(list);
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

function attachQueueDrag(list) {
  list.querySelectorAll('.qrow').forEach(row => {
    row.addEventListener('dragstart', e => {
      dragRow = row;
      row.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      try { e.dataTransfer.setData('text/plain', row.dataset.id || ''); } catch (_) {}
    });
    row.addEventListener('dragend', async () => {
      row.classList.remove('dragging');
      dragRow = null;
      const ids = [...list.querySelectorAll('.qrow')].map(r => r.dataset.id).filter(Boolean);
      try { await api.setOrder(ids); } catch (_) {}
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
    const after = queueDragAfter(list, e.clientY);
    if (after == null) list.appendChild(dragRow);
    else list.insertBefore(dragRow, after);
  });
}

async function renderHistory() {
  const [claims, stats, spark] = await Promise.all([api.claims(), api.stats(), api.spark()]);
  const v = $('#v-history');
  const hours = (stats.minutesWatched / 60).toFixed(1);
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">History &amp; report</h2><p class="sub">Everything the watcher has collected. Backed by the local SQLite database.</p></div>`));
  v.append(el(`<div class="stat-row">
    <div class="card stat"><div class="big ok">${stats.dropsClaimed}</div><div class="cap">Drops claimed</div></div>
    <div class="card stat"><div class="big">${hours}h</div><div class="cap">Watch time</div></div>
    <div class="card stat"><div class="big">${stats.successRate}%</div><div class="cap">Session success</div></div>
    <div class="card stat"><div class="big">${stats.sessions}</div><div class="cap">Sessions</div></div>
  </div>`));
  const grid = el('<div class="grid2"></div>');
  const claimsCard = el(`<div class="card mini"><h3>Recent claims</h3><div style="margin:0 -16px -15px">${claims.length ? claims.map(c => `
    <div class="clm"><span class="gift">🎁</span><div><div class="n">${esc(c.dropName)}</div><div class="g">${esc(c.game)}</div></div><div class="when">${esc(fmtDate(c.claimedAt))}</div></div>`).join('') : '<div class="empty">No claims yet.</div>'}</div></div>`);
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

const SETTINGS_PANES = [
  { key: 'games', label: 'Games', icon: '◵', tint: 'var(--ok-dim)', fg: '#4bbd85' },
  { key: 'player', label: 'Player', icon: '▸', tint: 'var(--accent-dim)', fg: '#7cc0ff' },
  { key: 'browser', label: 'Browser', icon: '◱', tint: 'var(--twitch-dim)', fg: '#b98cff' },
  { key: 'account', label: 'Account', icon: '◐', tint: 'rgba(127,127,127,0.16)', fg: 'var(--text-primary)' },
];

const toggleHTML = (id, on) => `<div class="switch ${on ? 'on' : ''}" role="switch" aria-checked="${on}" tabindex="0" data-toggle="${id}"><i></i></div>`;

async function renderSettings() {
  cfg = await api.config();
  const v = $('#v-settings');
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">Settings</h2><p class="sub">Saved to config.yaml. Browser changes prompt a Chrome restart.</p></div>`));
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
    case 'player': return `
      <div class="group"><div class="caption">Player</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Watch volume</div><div class="h">Kept low, never muted — a muted background tab stops crediting on Chrome 145+.</div></div>
          <div class="ctl" style="display:flex;align-items:center;gap:10px"><input class="input" type="number" min="0.005" max="1" step="0.005" value="${cfg.volume}" data-field="volume" style="width:96px"><span class="mono" id="vol-pct" style="color:var(--text-secondary);min-width:36px;text-align:right">${Math.round(cfg.volume * 100)}%</span></div></div>
        <div class="row"><div class="lab"><div class="l">Force lowest quality</div><div class="h">Saves bandwidth and CPU; does not affect drop credit.</div></div><div class="ctl">${toggleHTML('forceLowestQuality', cfg.forceLowestQuality)}</div></div>
        <div class="row"><div class="lab"><div class="l">Never focus the window</div><div class="h">Keep the watcher window in the background, dimmed, to avoid burn-in.</div></div><div class="ctl">${toggleHTML('neverFocus', cfg.neverFocus)}</div></div>
      </div></div>`;
    case 'browser': return `
      <div class="group"><div class="caption">Browser &amp; claiming</div><div class="card">
        <div class="row stacked"><div class="lab"><div class="l">Chrome path</div><div class="h">Blank = auto-detect. Override for Chrome Beta or an unusual install.</div></div>
          <div class="ctl"><input class="input mono" data-field="chromePath" value="${esc(cfg.chromePath)}" placeholder="auto-detected" style="width:100%"></div></div>
        <div class="row"><div class="lab"><div class="l">Skip account-link drops</div><div class="h">Surface drops that need a linked account instead of failing on them.</div></div><div class="ctl">${toggleHTML('skipAccountLink', cfg.skipAccountLink)}</div></div>
        <div class="row"><div class="lab"><div class="l">Allow GQL claim fallback</div><div class="h">Off by design — replaying the claim mutation has triggered bot blocks.</div></div><div class="ctl">${toggleHTML('allowGqlFallback', cfg.allowGqlFallback)}</div></div>
      </div></div>`;
    case 'account': return `
      <div class="group"><div class="caption">Account</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Auth mode</div><div class="h">Manual login is safest.</div></div><div class="ctl"><select class="popup" data-field="authMode"><option value="manual">Manual</option><option value="stored">Stored (unsupported)</option></select></div></div>
      </div>
      <div class="warnnote">⚠ Stored credentials are disabled by design and unsupported. Leave off unless manual login stops working.</div></div>`;
    default: return `
      <div class="group"><div class="caption">Games</div><div class="card">
        <div class="row"><div class="lab"><div class="l">Any game</div><div class="h">Queue drops for every game, ignoring the list below. Good for a quick test.</div></div><div class="ctl">${toggleHTML('anyGame', cfg.anyGame)}</div></div>
        <div class="row stacked"><div class="lab"><div class="l">Tracked games</div><div class="h">Only campaigns for these games are queued.</div></div>
          <div class="ctl" id="games-ctl"></div></div>
        <div class="row"><div class="lab"><div class="l">Auto-enqueue new campaigns</div><div class="h">Add newly discovered drops to the bottom of the queue.</div></div><div class="ctl">${toggleHTML('autoEnqueueNew', cfg.autoEnqueueNew)}</div></div>
        <div class="row"><div class="lab"><div class="l">Fall through to next available</div><div class="h">If the top drop has no live channel, watch the next available one and return to the top when its channel is live. Off = wait on the top drop.</div></div><div class="ctl">${toggleHTML('fallThrough', cfg.fallThrough)}</div></div>
      </div></div>`;
  }
}

function renderSettingsContent() {
  const host = $('#set-content');
  if (!host) return;
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
    cfg[f] = f === 'volume' ? parseFloat(inp.value) : inp.value;
    if (f === 'volume') { const p = $('#vol-pct', host); if (p) p.textContent = Math.round((parseFloat(inp.value) || 0) * 100) + '%'; }
  }));
  const authSel = host.querySelector('select[data-field="authMode"]');
  if (authSel) authSel.value = cfg.authMode;
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

async function saveSettings() {
  const msg = document.querySelector('#savemsg');
  try {
    await api.saveConfig(cfg);
    if (msg) { msg.textContent = 'Saved. Refreshing campaigns…'; msg.style.color = 'var(--ok)'; }
    api.refresh().catch(() => {});
    setTimeout(() => refreshAll(), 500);
  } catch (e) {
    if (msg) { msg.textContent = 'Error: ' + (e.message || e); msg.style.color = 'var(--err)'; }
  }
  setTimeout(() => { const m = document.querySelector('#savemsg'); if (m) m.textContent = ''; }, 4000);
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
    v.append(el(`<div><h2 class="title">Debugging</h2><p class="sub">Live runtime state and diagnostics. The exported log and JSON contain campaign data only — no auth tokens.</p></div>`));
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
    controls.querySelector('#dbg-discover').addEventListener('click', async () => { await api.refresh().catch(() => {}); flashMsg('#dbg-msg', 'discovery requested'); setTimeout(renderDebugPanel, 1000); });
    controls.querySelector('#dbg-copy-log').addEventListener('click', async () => {
      const ok = await copyText(((lastDebug && lastDebug.logTail) || []).join('\n'));
      flashMsg('#dbg-msg', ok ? 'log copied' : 'copy failed', ok ? 'var(--ok)' : 'var(--err)');
    });
    controls.querySelector('#dbg-copy-json').addEventListener('click', async () => {
      const ok = await copyText(JSON.stringify(lastDebug || {}, null, 2));
      flashMsg('#dbg-msg', ok ? 'JSON copied' : 'copy failed', ok ? 'var(--ok)' : 'var(--err)');
    });
    const dumpSw = controls.querySelector('[data-toggle="captureGqlBodies"]');
    if (dumpSw) dumpSw.addEventListener('click', async () => {
      const on = !dumpSw.classList.contains('on');
      dumpSw.classList.toggle('on', on);
      if (!cfg) cfg = await api.config();
      cfg.captureGqlBodies = on;
      try { await api.saveConfig(cfg); flashMsg('#dbg-msg', on ? 'dumping enabled' : 'dumping disabled'); }
      catch (e) { flashMsg('#dbg-msg', 'save failed', 'var(--err)'); }
    });
  }
  await renderDebugPanel();
}

async function renderDebugPanel() {
  const host = document.querySelector('#debug-panel');
  if (!host) return;
  let d;
  try { d = await api.debug(); lastDebug = d; }
  catch (e) { host.innerHTML = '<div class="card mini"><div class="empty">Debug info needs the backend running (not a static preview).</div></div>'; return; }
  const st = d.status || {};
  const disc = d.lastDiscovery;
  const elig = d.eligibility || {};
  const integrity = ((disc && disc.headerKeys) || []).indexOf('client-integrity') !== -1;
  const paths = d.paths || {};
  const eligStr = Object.keys(elig).length ? Object.entries(elig).map(([k, v]) => `${cap(k)} ${v}`).join('   ') : '—';
  const runLine = `${d.runtimeUp ? 'up' : 'starting'} · ${esc(st.state || '—')}${st.channelLogin ? ' @' + esc(st.channelLogin) : ''}`;
  const discLine = disc ? `gql ${disc.gqlResponses} · parsed ${disc.campaignsParsed} · details ${disc.detailsParsed} · enqueued ${disc.enqueued}` : 'not run yet';
  const logText = (d.logTail || []).slice(-120).join('\n') || 'no log lines yet';
  host.innerHTML = `<div class="card mini">
    <div class="kv"><span class="k">Runtime</span><span class="v">${runLine}</span></div>
    <div class="kv"><span class="k">Eligibility</span><span class="v mono">${esc(eligStr)}</span></div>
    <div class="kv"><span class="k">Last discovery</span><span class="v mono">${esc(discLine)}</span></div>
    <div class="kv"><span class="k">Integrity token captured</span><span class="v" style="color:${integrity ? 'var(--ok)' : 'var(--err)'}">${integrity ? 'yes' : 'no'}</span></div>
    <div class="kv"><span class="k">Debug folder</span><span class="v mono" style="font-size:11px;max-width:62%;overflow:hidden;text-overflow:ellipsis" title="${esc(paths.debugDir || '')}">${esc(paths.debugDir || '')}</span></div>
    <div class="log-head"><div class="caption" style="margin:0">Log tail</div><button class="btn ghost btn-sm" id="dbg-log-copy" type="button">Copy</button></div>
    <pre id="dbg-log" class="log-pre">${esc(logText)}</pre>
  </div>`;
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

function init() {
  document.querySelectorAll('.nav').forEach(n => n.addEventListener('click', () => go(n.dataset.view)));
  // Load config first so cfg is available (theme), then render everything.
  api.config().then(c => {
    cfg = c;
    if (cfg && cfg.theme && cfg.theme !== 'system') document.documentElement.setAttribute('data-theme', cfg.theme);
    else document.documentElement.removeAttribute('data-theme');
    refreshAll();
  }).catch(() => refreshAll());
  // Live updates: re-poll periodically; live builds also get pushed events.
  setInterval(() => {
    if (isActive('dashboard')) { renderDashboard(); }
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
