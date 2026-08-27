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
    const row = el(`<div class="qrow ${q.status === 'active' ? 'active' : ''}">
      <span class="grip" title="drag to reorder">⁝⁝</span>
      <div class="qname"><div class="n">${esc(q.dropName || q.dropID)}</div><div class="meta">${esc(q.game)}${q.reason ? ' · ' + esc(q.reason) : ''}</div></div>
      <div class="qmini"><div class="bar"><i class="${q.status === 'active' ? '' : 'blue'}" style="width:${pct(q.currentMinutes, q.requiredMinutes)}%"></i></div><div class="mm mono">${q.requiredMinutes ? q.currentMinutes + ' / ' + q.requiredMinutes + ' min' : '—'}</div></div>
      <span class="pill ${esc(q.status)}"><span class="d"></span> ${esc(cap(q.status))}</span>
      <div class="switch ${q.enabled ? 'on' : ''}" role="switch" aria-checked="${q.enabled}"><i></i></div>
    </div>`);
    row.querySelector('.switch').addEventListener('click', async (e) => {
      const on = !e.currentTarget.classList.contains('on');
      e.currentTarget.classList.toggle('on', on);
      await api.setEnabled(q.dropID, on);
    });
    list.append(row);
  }
  v.append(list);
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

async function renderSettings() {
  cfg = await api.config();
  const v = $('#v-settings');
  v.innerHTML = '';
  v.append(el(`<div><h2 class="title">Settings</h2><p class="sub">Written to config.yaml. Browser changes prompt a Chrome restart.</p></div>`));

  const toggle = (id, on) => `<div class="switch ${on ? 'on' : ''}" role="switch" aria-checked="${on}" data-toggle="${id}"><i></i></div>`;
  const wrap = el(`<div>
    <div class="group"><div class="caption">Games</div><div class="card">
      <div class="row"><div class="lab"><div class="l">Any game</div><div class="h">Queue drops for every game, ignoring the list below. Good for a quick test.</div></div><div class="ctl">${toggle('anyGame', cfg.anyGame)}</div></div>
      <div class="row stacked" id="games-row"><div class="lab"><div class="l">Tracked games</div><div class="h">Only campaigns for these games are queued.</div></div>
        <div class="ctl" id="games-ctl" style="width:100%;margin-top:8px"></div></div>
      <div class="row"><div class="lab"><div class="l">Auto-enqueue new campaigns</div><div class="h">Add newly discovered drops to the bottom of the queue.</div></div><div class="ctl">${toggle('autoEnqueueNew', cfg.autoEnqueueNew)}</div></div>
    </div></div>

    <div class="group"><div class="caption">Player</div><div class="card">
      <div class="row"><div class="lab"><div class="l">Watch volume</div><div class="h">Kept low, never muted — a muted background tab stops crediting on Chrome 145+.</div></div>
        <div class="ctl" style="display:flex;align-items:center;gap:10px"><input class="input" type="number" min="0.005" max="1" step="0.005" value="${cfg.volume}" data-field="volume" style="width:90px"><span class="mono" style="color:var(--text-secondary)">${Math.round(cfg.volume * 100)}%</span></div></div>
      <div class="row"><div class="lab"><div class="l">Force lowest quality</div><div class="h">Saves bandwidth and CPU; does not affect drop credit.</div></div><div class="ctl">${toggle('forceLowestQuality', cfg.forceLowestQuality)}</div></div>
      <div class="row"><div class="lab"><div class="l">Never focus the window</div><div class="h">Keep the watcher window in the background, dimmed, to avoid burn-in.</div></div><div class="ctl">${toggle('neverFocus', cfg.neverFocus)}</div></div>
    </div></div>

    <div class="group"><div class="caption">Browser &amp; claiming</div><div class="card">
      <div class="row"><div class="lab"><div class="l">Chrome path</div><div class="h">Blank = auto-detect.</div></div><div class="ctl"><input class="input mono" data-field="chromePath" value="${esc(cfg.chromePath)}" placeholder="auto-detected"></div></div>
      <div class="row"><div class="lab"><div class="l">Skip account-link drops</div><div class="h">Surface drops that need a linked account instead of failing on them.</div></div><div class="ctl">${toggle('skipAccountLink', cfg.skipAccountLink)}</div></div>
      <div class="row"><div class="lab"><div class="l">Allow GQL claim fallback</div><div class="h">Off by design — replaying the claim mutation has triggered bot blocks.</div></div><div class="ctl">${toggle('allowGqlFallback', cfg.allowGqlFallback)}</div></div>
    </div></div>

    <div class="group"><div class="caption">Account</div><div class="card">
      <div class="row"><div class="lab"><div class="l">Auth mode</div><div class="h">Manual login is safest.</div></div><div class="ctl"><select class="popup" data-field="authMode"><option value="manual">Manual</option><option value="stored">Stored (unsupported)</option></select></div></div>
    </div>
    <div class="warnnote">⚠ Stored credentials are disabled by design and unsupported. Leave off unless manual login stops working.</div></div>

    <div class="group"><div class="caption">Diagnostics</div><div class="card">
      <div class="row"><div class="lab"><div class="l">Dump GraphQL bodies</div><div class="h">Writes campaign JSON (no auth) to ~/.dropwarden/debug/ on each discovery, for troubleshooting.</div></div><div class="ctl">${toggle('captureGqlBodies', cfg.captureGqlBodies)}</div></div>
    </div></div>

    <div class="foot-btns"><button class="btn primary" id="save">Save changes</button><button class="btn" id="reset">Reset to defaults</button><span id="savemsg" style="align-self:center;color:var(--text-secondary);font-size:12px"></span></div>
  </div>`);

  $('select[data-field="authMode"]', wrap).value = cfg.authMode;
  wrap.querySelectorAll('[data-toggle]').forEach(sw => sw.addEventListener('click', () => {
    const on = !sw.classList.contains('on'); sw.classList.toggle('on', on); cfg[sw.dataset.toggle] = on;
  }));
  wrap.querySelectorAll('[data-field]').forEach(inp => inp.addEventListener('input', () => {
    const f = inp.dataset.field; cfg[f] = f === 'volume' ? parseFloat(inp.value) : inp.value;
  }));
  // Games manager: add/remove chips, dim when Any game is on.
  const gamesCtl = $('#games-ctl', wrap);
  function renderGames() {
    const disabled = cfg.anyGame;
    gamesCtl.style.opacity = disabled ? '0.45' : '1';
    gamesCtl.style.pointerEvents = disabled ? 'none' : 'auto';
    const chips = (cfg.games || []).map((g, i) => `<span class="chip">${esc(g)} <span class="x" data-rm="${i}" style="cursor:pointer">×</span></span>`).join('');
    gamesCtl.innerHTML = `<div class="chips" style="justify-content:flex-start">${chips || '<span style="color:var(--text-tertiary);font-size:12px">no games — add one or turn on Any game</span>'}</div>
      <div style="display:flex;gap:8px;margin-top:8px"><input class="input" id="game-add" placeholder="Exact game name, e.g. Special Events" style="flex:1"><button class="btn" id="game-add-btn" type="button">Add</button></div>`;
    gamesCtl.querySelectorAll('[data-rm]').forEach(x => x.addEventListener('click', () => { cfg.games.splice(+x.dataset.rm, 1); renderGames(); }));
    const addInput = gamesCtl.querySelector('#game-add');
    const addGame = () => { const val = addInput.value.trim(); if (val && !cfg.games.includes(val)) cfg.games.push(val); renderGames(); };
    gamesCtl.querySelector('#game-add-btn').addEventListener('click', addGame);
    addInput.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addGame(); } });
  }
  renderGames();
  const anyToggle = wrap.querySelector('[data-toggle="anyGame"]');
  if (anyToggle) anyToggle.addEventListener('click', renderGames);

  $('#save', wrap).addEventListener('click', async () => {
    const msg = $('#savemsg', wrap);
    try {
      await api.saveConfig(cfg);
      msg.textContent = 'Saved. Refreshing campaigns…';
      msg.style.color = 'var(--ok)';
      api.refresh().catch(() => {});
      setTimeout(() => { refreshAll(); }, 500);
    } catch (e) {
      msg.textContent = 'Error: ' + (e.message || e);
      msg.style.color = 'var(--err)';
    }
    setTimeout(() => { msg.textContent = ''; }, 4000);
  });
  $('#reset', wrap).addEventListener('click', () => renderSettings());
  v.append(wrap);
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

function init() {
  document.querySelectorAll('.nav').forEach(n => n.addEventListener('click', () => switchView(n.dataset.view)));
  // Load settings first so cfg is available (theme, priority default).
  renderSettings().then(() => {
    if (cfg && cfg.theme && cfg.theme !== 'system') document.documentElement.setAttribute('data-theme', cfg.theme);
    else document.documentElement.removeAttribute('data-theme');
    refreshAll();
  });
  // Live updates: re-poll periodically; live builds also get pushed events.
  setInterval(() => { renderDashboard(); renderFoot(); }, 5000);
  api.onEvent('watcher:progress', () => renderDashboard());
  api.onEvent('watcher:state', () => { renderDashboard(); renderFoot(); });
  api.onEvent('drop:claimed', () => refreshAll());
}

init();
