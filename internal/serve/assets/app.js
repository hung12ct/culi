/* culi Context Control — embedded product UI.
   No framework, no build step: served by `culi serve` from embed.FS, and opens
   standalone (file://) via the SEED fallback when /api is unreachable.

   Rendering contract: discrete actions (navigate, approve/reject, toggle facet,
   select, open/close edit) re-render targeted regions. Text inputs are
   UNCONTROLLED — read from the DOM on save — so typing never triggers a
   re-render and never loses focus. */

'use strict';

// ---------- product tokens ----------
const COLOR = {
  primary: '#4f6fed', success: '#169b6b', warning: '#c97a16', danger: '#dc4c4c', neutral: '#7c8799',
};
const TYPE = {
  rule:   ['R', '#3973db'], style:  ['S', '#0f8ca8'], lesson: ['L', '#c97a16'],
  pattern:['P', '#7c5ce0'], skill:  ['K', '#169b6b'], agent:  ['A', '#d65383'],
};
const STATUS = {
  confirmed: ['Confirmed', 'confirmed'], candidate: ['Candidate', 'candidate'], retired: ['Retired', 'retired'],
};
const STATUS_COLOR = { confirmed: COLOR.success, candidate: COLOR.warning, retired: COLOR.neutral };

const ICONS = {
  home: '<path d="M3.5 10.5 12 3l8.5 7.5"/><path d="M5.5 9.5V21h13V9.5M9.5 21v-7h5v7"/>',
  review: '<rect x="3" y="3" width="18" height="18" rx="4"/><path d="m7.5 12 3 3 6-6"/>',
  knowledge: '<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H11v17H6.5A2.5 2.5 0 0 0 4 22Z"/><path d="M20 5.5A2.5 2.5 0 0 0 17.5 3H13v17h4.5A2.5 2.5 0 0 1 20 22Z"/>',
  activity: '<path d="M3 12h4l2.5-6 5 12 2.5-6h4"/><path d="M4 4v16h16"/>',
  reconcile: '<path d="M4 7h11"/><path d="m11 4 3 3-3 3"/><path d="M20 17H9"/><path d="m13 20-3-3 3-3"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.86 2.86-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1V21H9.55v-.09A1.7 1.7 0 0 0 8.4 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.86-2.86.06-.06A1.7 1.7 0 0 0 4 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1-.4H2.3V9.55h.09A1.7 1.7 0 0 0 4 8.4a1.7 1.7 0 0 0-.34-1.88l-.06-.06L6.46 3.6l.06.06A1.7 1.7 0 0 0 8.4 4a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1V2.3h4.05v.09A1.7 1.7 0 0 0 15 4a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.86 2.86-.06.06A1.7 1.7 0 0 0 19.4 8.4a1.7 1.7 0 0 0 .6 1 1.7 1.7 0 0 0 1 .4h.09v4.05H21a1.7 1.7 0 0 0-1.6 1.15Z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>',
};

function uiIcon(name) {
  return `<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${ICONS[name] || ''}</svg>`;
}

function alpha(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
function scopeTier(label) {
  if (label.startsWith('lang:')) return 'lang';
  if (label.startsWith('repo:')) return 'repo';
  if (label.startsWith('branch:')) return 'branch';
  return 'global';
}
function typeIcon(type, cls) {
  const [letter, color] = TYPE[type] || ['?', COLOR.neutral];
  return `<span class="ticon ${cls || ''} mono" style="background:${alpha(color, 0.16)};color:${color};">${letter}</span>`;
}
function scopeChip(label, lg) {
  return `<span class="chip ${lg ? 'chip-lg' : ''} scope-${scopeTier(label)}">${esc(label)}</span>`;
}
function scopeChips(labels, lg) { return (labels || []).map(l => scopeChip(l, lg)).join(''); }
function fmtCount(n) { return Number(n || 0).toLocaleString(); }

// Effectiveness verdict badges — buckets computed server-side from decayed
// feedback counters + 7d token cost (see store.ClassifyEffectiveness).
const EFF = {
  helpful:   ['Helpful',   COLOR.success],
  noisy:     ['Noisy',     COLOR.danger],
  expensive: ['Expensive', COLOR.warning],
  uncertain: ['Uncertain', COLOR.neutral],
};
function effBadge(bucket, note) {
  const [label, color] = EFF[bucket] || EFF.uncertain;
  return `<span class="eff-badge" style="background:${alpha(color, 0.14)};color:${color};"${note ? ` title="${esc(note)}"` : ''}>${label}</span>`;
}

// destrBtn renders a destructive button that arms on first click. When its act
// is the one awaiting confirmation, it flips to a pulsing red "Confirm?" —
// an inline two-step guard instead of a modal (keeps the design's fast feel).
function destrBtn(act, label, cls) {
  if (state.confirm === act) return `<button class="btn btn-confirm" data-act="${esc(act)}">Confirm?</button>`;
  return `<button class="${cls}" data-act="${esc(act)}">${esc(label)}</button>`;
}

// ---------- app state ----------
const state = {
  screen: 'overview',
  queueIndex: 0,
  reviewed: 0,
  kbId: null,
  kbMode: 'cards',
  kbSearch: '',
  kbEditing: false,
  typeFilter: {},
  statusFilter: {},
  staleFilter: false, // KB "Stale" facet — show only never-pulled cards
  returnTo: null,     // screen to offer a "← back" link to after an Overview drill-down
  actTab: 'inj',
  actRepo: '',        // Activity: repo filter ('' = all repos)
  actHarness: '',     // Activity: harness filter ('' = all harnesses)
  actSince: 'all',    // Activity: date filter (all | today | 7d | 30d)
  actRepos: [],       // repo labels seen in the recent window (filter dropdown options)
  actHarnesses: [],   // [{code,label}] harnesses seen in the recent window (filter options)
  injOpen: {}, // keyed "si:ei" — which injection events are expanded
  toast: null,
  undoStack: null,
  confirm: null, // act string of a destructive button awaiting a second click
  reposOpen: false,
  repos: null, // [{path,name,exists,isGit}] loaded from /api/repos
  analytics: null,
  analyticsLoading: false,
  analyticsRequest: 0,
  analyticsHarness: '',
  analyticsRepo: '',
  // data caches (populated from /api or SEED)
  status: null, overview: null, candidates: null,
  cards: null, cardDetail: {}, sessions: null, runs: null, settings: null,
  settingsDraft: null,
  reconcile: null,        // /api/import payload
  recForce: false,        // apply with --force (overwrite conflicts)
  recOpen: {},            // expanded conflict/residual keys
  recBusy: '',            // in-flight reconcile action label (disables buttons)
  recPoll: null,          // merge progress poll timer
};
let toastTimer = null;
let confirmTimer = null;
const THEME_KEY = 'culi-theme';

function syncThemeControl() {
  const btn = document.getElementById('theme-toggle');
  if (!btn) return;
  const dark = document.documentElement.dataset.theme === 'dark';
  const label = dark ? 'Use light mode' : 'Use dark mode';
  btn.title = label;
  btn.setAttribute('aria-label', label);
}

function setTheme(theme, persist) {
  document.documentElement.dataset.theme = theme;
  if (persist) {
    try { localStorage.setItem(THEME_KEY, theme); } catch (_) { /* private mode can deny storage */ }
  }
  syncThemeControl();
}

function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  setTheme(next, true);
}

// ---------- api layer (falls back to SEED on any failure) ----------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}
async function loadStatus()    { try { return await api('/api/status'); }    catch { return SEED.status; } }
async function loadOverview()  { try { return await api('/api/overview'); }  catch { return SEED.overview; } }
async function loadAnalytics() {
  const q = new URLSearchParams();
  if (state.analyticsHarness) q.set('harness', state.analyticsHarness);
  if (state.analyticsRepo) q.set('repo', state.analyticsRepo);
  const qs = q.toString();
  try { return await api('/api/analytics' + (qs ? '?' + qs : '')); }
  catch {
    if (typeof location !== 'undefined' && location.protocol === 'file:') return SEED.analytics;
    return { available: false, reason: 'Activity data is temporarily unavailable', range: 'Last 7 days', cards: [], repos: [], harnesses: [] };
  }
}
async function loadCandidates(){ try { return await api('/api/candidates'); }catch { return SEED.candidates; } }
async function loadCards()     { try { return await api('/api/cards'); }     catch { return SEED.cards; } }
async function loadCard(id)    { try { return await api('/api/cards/' + encodeURIComponent(id)); } catch { return (SEED.cards.find(c => c.id === id) || null); } }
async function loadSessions()  {
  const q = new URLSearchParams();
  if (state.actRepo) q.set('repo', state.actRepo);
  if (state.actHarness) q.set('harness', state.actHarness);
  if (state.actSince && state.actSince !== 'all') q.set('since', state.actSince);
  const qs = q.toString();
  try { return await api('/api/activity/injections' + (qs ? '?' + qs : '')); }
  catch {
    // Offline demo only (no server): derive harness options from the seed data
    // so the dropdown never lists an agent absent from the shown sessions. The
    // live path always gets server-authored labels; this map is the sole
    // exception, unavoidable without a backend.
    const labels = { claude: 'Claude Code', codex: 'Codex CLI' };
    const codes = [...new Set((SEED.sessions || []).map(s => s.harness).filter(Boolean))];
    return { sessions: SEED.sessions, repos: [], harnesses: codes.map(c => ({ code: c, label: labels[c] || c })) };
  }
}
async function loadRuns()      { try { return await api('/api/activity/runs'); } catch { return SEED.runs; } }
async function loadSettings()  { try { return await api('/api/config'); }     catch { return SEED.settings; } }
async function loadImport()    { try { return await api('/api/import'); }     catch { return SEED.import; } }

async function postAction(path) {
  // Best-effort write; UI is optimistic and git-backed on the server side.
  try { await fetch(path, { method: 'POST' }); } catch { /* offline prototype: no-op */ }
}

// postJSON POSTs a JSON body and returns the parsed response (or an offline
// marker). Used by the card actions, which need the server's ok/note back.
async function postJSON(path, body) {
  try {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    });
    return await res.json().catch(() => ({ ok: true }));
  } catch { return { ok: false, note: 'offline — action not applied' }; }
}

// ---------- shell rendering ----------
const NAV = [
  { key: 'overview', label: 'Home',           icon: 'home' },
  { key: 'review',   label: 'Review',         icon: 'review' },
  { key: 'kb',       label: 'Knowledge',      icon: 'knowledge' },
  { key: 'activity', label: 'Activity',       icon: 'activity' },
  { key: 'reconcile',label: 'Reconcile',      icon: 'reconcile' },
  { key: 'settings', label: 'Settings',       icon: 'settings' },
];
const TITLES = {
  overview: ['Home', 'your context system at a glance'],
  review:   ['Review', () => `${(state.candidates || []).length} proposed lessons waiting for you`],
  kb:       ['Knowledge', () => state.kbMode === 'analytics' ? 'delivery analytics across agents and repositories' : `${(state.status && state.status.cards) || (state.cards || []).length} reusable cards`],
  activity: ['Activity', 'see what Culi sent, learned, and why'],
  reconcile:['Reconcile', 'fold duplicated rules & skills across repos into one canonical set'],
  settings: ['Settings', 'tune retrieval and learning safely'],
};

function renderStrip() {
  const st = state.status || {};
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v; };
  set('s-version', st.version || '—');
  set('s-cards', st.cards != null ? st.cards : '—');
  set('s-saved', st.savedPct != null ? st.savedPct : '—');
  set('s-review', (state.candidates ? state.candidates.length : (st.toReview != null ? st.toReview : '—')));
  set('s-learn', st.toLearn != null ? st.toLearn : '—');
  const dot = document.getElementById('serve-dot');
  if (dot) dot.classList.toggle('down', st.serveDown === true);
  const failed = document.getElementById('s-failed');
  const n = st.failedJobs || 0;
  if (failed) {
    failed.hidden = n <= 0;
    document.getElementById('s-failed-label').textContent = `${n} failed job${n === 1 ? '' : 's'}`;
  }
}
function renderNav() {
  const wrap = document.getElementById('nav-items');
  const st = state.status || {};
  const nCand = state.candidates ? state.candidates.length : (st.toReview || 0);
  wrap.innerHTML = NAV.map(n => {
    const active = state.screen === n.key;
    const badge = n.key === 'review' && nCand > 0 ? `<span class="nav-badge">${nCand}</span>` : '';
    return `<button class="nav-item ${active ? 'active' : ''}" data-act="nav:${n.key}">
      <span class="nav-icon">${uiIcon(n.icon)}</span><span class="nav-label">${n.label}</span>${badge}
    </button>`;
  }).join('');
}
function renderSpend() {
  const st = state.status || {};
  const spent = st.spendToday != null ? st.spendToday : 0.14;
  const cap = st.spendCap != null ? st.spendCap : 1.0;
  const pct = cap > 0 ? Math.min(100, Math.round((spent / cap) * 100)) : 0;
  document.getElementById('spend-amt').textContent = '$' + spent.toFixed(2);
  document.getElementById('spend-fill').style.width = pct + '%';
  document.getElementById('spend-cap').textContent = `${pct}% of $${cap.toFixed(2)} daily cap`;
}
function renderHeader() {
  const [title, sub] = TITLES[state.screen];
  document.getElementById('screen-title').textContent = title;
  document.getElementById('screen-sub').textContent = typeof sub === 'function' ? sub() : sub;
  const back = document.getElementById('screen-back');
  if (back) {
    const show = !!state.returnTo && state.returnTo !== state.screen;
    back.hidden = !show;
    if (show) back.textContent = '← ' + ((TITLES[state.returnTo] || ['Back'])[0]);
  }
}

// ---------- Knowledge Pulse ----------
function harnessClass(code) { return String(code || 'other').toLowerCase().replace(/[^a-z0-9_-]/g, '-'); }

function analyticsFilters(a) {
  const harnesses = (a && a.harnesses) || [];
  const harnessButtons = [{ code: '', label: 'All agents' }, ...harnesses].map(h =>
    `<button class="pulse-filter ${state.analyticsHarness === h.code ? 'active' : ''}" data-act="analyticsHarness:${esc(h.code || 'all')}">${esc(h.label)}</button>`
  ).join('');
  const repoOptions = ['<option value="">All repositories</option>'].concat(
    (((a && a.repos) || []).map(r => `<option value="${esc(r.key)}"${state.analyticsRepo === r.key ? ' selected' : ''}>${esc(r.label)}</option>`))
  ).join('');
  return `<div class="pulse-filters" aria-label="Knowledge Pulse filters">
    <div class="pulse-filter-group">${harnessButtons}</div>
    <select class="pulse-repo" data-change="analyticsRepo" aria-label="Filter Knowledge Pulse by repository">${repoOptions}</select>
  </div>`;
}

function analyticsChart(a, limit) {
  const cards = ((a && a.cards) || []).slice(0, limit || 10);
  if (!cards.length) return `<div class="pulse-empty"><span class="pulse-empty-mark">↗</span><b>No cards delivered in this view</b><span>Try another agent or repository. Gate skips are not counted as deliveries.</span></div>`;
  const max = Math.max(1, ...cards.map(c => Number(c.sessions || 0)));
  return `<div class="pulse-chart">${cards.map((c, i) => {
    const segments = (c.harnesses || []).map(h =>
      `<span class="pulse-bar-segment harness-${harnessClass(h.code)}" style="width:${Math.max(3, (Number(h.sessions || 0) / max) * 100)}%" title="${esc(h.label)} · ${fmtCount(h.sessions)} sessions · ${fmtCount(h.deliveries)} deliveries"></span>`
    ).join('');
    const name = c.available
      ? `<button class="pulse-card-link" data-act="analyticsCard:${esc(c.short)}" title="Open ${esc(c.id)}">${esc(c.title)}</button>`
      : `<span class="pulse-card-link unavailable" title="This card is no longer in the knowledge base">${esc(c.title)}</span>`;
    return `<div class="pulse-chart-row">
      <span class="pulse-rank mono">${i + 1}</span>
      <div class="pulse-card-name">${name}<span class="pulse-card-meta">${esc(c.type)}${c.repos && c.repos.length ? ' · ' + esc(c.repos.join(', ')) : ''}${c.available ? '' : ' · removed'}</span></div>
      <div class="pulse-bar-track">${segments}</div>
      <div class="pulse-row-stat"><b>${fmtCount(c.sessions)}</b><span>session${Number(c.sessions) === 1 ? '' : 's'}</span></div>
    </div>`;
  }).join('')}</div>`;
}

function analyticsStatus(a, body) {
  if (state.analyticsLoading && !a) return `<div class="pulse-loading"><span></span><span></span><span></span></div>`;
  if (a && a.available === false) return `<div class="pulse-unavailable"><b>Knowledge Pulse is unavailable</b><span>${esc(a.reason || 'Activity data could not be read. Refresh to try again.')}</span></div>`;
  return body;
}

function homeAnalytics() {
  const a = state.analytics;
  const summary = (a && a.summary) || {};
  return `<section class="pulse-card card" aria-labelledby="pulse-home-title">
    <div class="pulse-head">
      <div><div class="pulse-eyebrow">Knowledge Pulse · ${esc((a && a.range) || 'Last 7 days')}</div>
        <h3 id="pulse-home-title">Hot cards across your coding agents</h3>
        <p>Ranked by distinct sessions, then deliveries and recency.</p></div>
      <button class="btn btn-outline pulse-open" data-act="kbAnalytics">Open analytics →</button>
    </div>
    ${analyticsFilters(a)}
    ${analyticsStatus(a, `<div class="pulse-summary-line"><b>${fmtCount(summary.activeCards)}</b> cards used <span>·</span> <b>${fmtCount(summary.sessions)}</b> sessions <span>·</span> <b>${fmtCount(summary.tokens)}</b> tokens delivered</div>${analyticsChart(a, 5)}`)}
  </section>`;
}

// ---------- overview ----------
function screenOverview() {
  const o = state.overview || SEED.overview;
  const st = state.status || {};
  const cf = o.cf || { injected: 0, sessions: 0, vsAll: 0 };
  // Go marshals empty slices as JSON null (not []), so any of these can arrive
  // null from /api/overview. Guard every .map — one throw here aborts
  // renderScreen() *after* renderHeader() ran, leaving the previous screen's
  // body under the new title.
  const trend = (o.trend || []).map(h => `<div class="b" style="height:${h}%"></div>`).join('');
  const tiles = (o.tiles || []).map(t =>
    `<div class="tile"><div class="tile-label">${esc(t.label)}</div>
      <div class="tile-val" style="color:${t.color}">${esc(t.value)}</div>
      <div class="tile-sub">${esc(t.sub)}</div></div>`).join('');
  const g = o.granularity;
  const failedCount = (o.failedJobs || []).length;
  const reviewCount = (state.candidates || []).length || st.toReview || 0;
  const hasInjectionHistory = Number(String(cf.sessions || 0).replace(/,/g, '')) > 0;
  const healthTitle = failedCount ? 'Learning needs attention' : 'Culi is working in the background';
  const healthCopy = failedCount
    ? 'Context delivery is still available. Check the failed learning job when you are ready.'
    : 'Relevant knowledge is ready for your coding agents. You stay in control of what Culi keeps.';
  const failed = (o.failedJobs && o.failedJobs.length) ? `
    <div class="failed">
      <div class="failed-head"><span class="ic">⚠</span><span class="tt">${o.failedJobs.length} failed job${o.failedJobs.length === 1 ? '' : 's'}</span></div>
      <div class="failed-line">${esc(o.failedJobs[0].kind)} — <span class="mono">${esc(o.failedJobs[0].at)}</span></div>
      <div class="failed-sub">${esc(o.failedJobs[0].reason)}</div>
      <div class="failed-help mono">Run: culi learn</div>
    </div>` : '';
  const noisy = (o.noisy || []).map(c => {
    const nm = c.short
      ? `<span class="attn-name link" data-act="ovCard:${esc(c.short)}" role="button" tabindex="0" title="Open ${esc(c.name)} in the Knowledge Base">${esc(c.name)}</span>`
      : `<span class="attn-name">${esc(c.name)}</span>`;
    return `<div class="attn-row">${nm}
      <span class="score-pill" title="Tokens this card cost in the injection window while never being pulled">${esc(c.score)}</span>
      <button class="btn btn-outline" data-act="down:${esc(c.id || '')}">Down</button>
      ${destrBtn('reject-noisy:' + (c.id || ''), 'Retire', 'btn btn-red-outline')}</div>`;
  }).join('') || `<div class="attn-empty">No noisy cards — nothing is being injected repeatedly and left unpulled.</div>`;
  const stale = (o.stale || []).map(c => {
    const link = c.short ? `data-act="ovCard:${esc(c.short)}" role="button" tabindex="0" title="Open ${esc(c.name)} in the Knowledge Base"` : '';
    return `<div class="attn-row ${c.short ? 'link' : ''}" ${link}>
      <span class="attn-name" style="color:var(--muted)">${esc(c.name)}</span>
      <span class="attn-last">last ${esc(c.last)}</span></div>`;
  }).join('');
  return `<div class="ov">
    <div class="home-hero ${failedCount ? 'warn' : ''}">
      <div class="home-status">
        <div class="health-label"><span class="health-dot"></span>${failedCount ? 'Needs attention' : 'System healthy'}</div>
        <h2>${healthTitle}</h2>
        <p>${healthCopy}</p>
        <div class="home-tags"><span>Local-first</span><span>Cross-harness</span><span>${esc(st.toLearn || 0)} queued to learn</span></div>
        <div class="home-actions">
          ${reviewCount ? `<button class="btn home-primary" data-act="nav:review">Review ${reviewCount} lesson${reviewCount === 1 ? '' : 's'}</button>` : `<button class="btn home-primary" data-act="nav:kb">Browse knowledge</button>`}
          <button class="btn btn-outline" data-act="nav:activity">View activity</button>
        </div>
      </div>
      <div class="efficiency">
        <div class="efficiency-label">Context avoided · last 7 days</div>
        ${hasInjectionHistory ? `
          <div class="hero-num-row"><div class="hero-num">${esc(o.savedPct)}</div><div class="hero-spark">${trend}</div></div>
          <div class="hero-cf" title="Culi compares targeted injection with loading the whole knowledge base every session."><b>${esc(cf.injected)}</b> tokens delivered across <b>${esc(cf.sessions)}</b> sessions instead of <b>${esc(cf.vsAll)}</b>.</div>
        ` : `
          <div class="efficiency-empty-title">No injection history yet</div>
          <div class="efficiency-empty-copy">This metric appears after Culi delivers relevant knowledge to Claude or Codex. Gate skips do not count as injections.</div>
        `}
      </div>
    </div>
    <div class="tiles home-tiles">${tiles}</div>
    ${homeAnalytics()}
    <div class="ov-row">
      <div class="card gran">
        <div class="gran-head"><div class="gran-title">Injection granularity</div><div class="gran-note">packer is degrading gracefully</div></div>
        <div class="gran-bar">
          <div style="width:${g.body}%;background:var(--pink)"></div>
          <div style="width:${g.summary}%;background:var(--blue)"></div>
          <div style="width:${g.hook}%;background:var(--teal)"></div>
        </div>
        <div class="gran-legend">
          <div class="gran-leg"><span class="sw" style="background:var(--pink)"></span>body <b>${g.body}%</b></div>
          <div class="gran-leg"><span class="sw" style="background:var(--blue)"></span>summary <b>${g.summary}%</b></div>
          <div class="gran-leg"><span class="sw" style="background:var(--teal)"></span>hook <b>${g.hook}%</b></div>
        </div>
      </div>
      ${failed}
    </div>
    <div class="ov-row" style="margin-bottom:0">
      <div class="card attn">
        <div class="attn-head"><div class="attn-title">Noisy cards</div><div class="attn-note">${esc(o.noisyHeader || 'injected · never pulled')}</div></div>
        ${noisy}
        ${o.noisyMore ? `<div class="attn-more">${esc(o.noisyMore)}</div>` : ''}
      </div>
      <div class="card attn">
        <div class="attn-head"><div class="attn-title">Stale cards</div><a data-act="ovStale">${esc(o.staleHeader || '0 never pulled (30d)')} →</a></div>
        ${stale || `<div class="attn-empty">No stale cards — every live card has been injected recently.</div>`}
        ${o.staleMore ? `<div class="attn-more link" data-act="ovStale">${esc(o.staleMore)} →</div>` : ''}
      </div>
    </div>
  </div>`;
}

// ---------- review ----------
function screenReview() {
  const cands = state.candidates || [];
  const rows = cands.map((c, i) => {
    const sel = i === state.queueIndex;
    return `<button class="q-row ${sel ? 'sel' : ''}" data-act="selCand:${i}">
      <div class="q-row-top">${typeIcon(c.type, 'ticon-16')}
        <span class="q-shortid">${esc(c.id)}</span><span class="spacer"></span>
        <span class="draft-tag">PROPOSED</span></div>
      <div class="q-title">${esc(c.title)}</div>
      <div class="q-scopes">${scopeChips(c.scopeLabels)}<span class="q-ago">${esc(c.ago)}</span></div>
    </button>`;
  }).join('');
  const queueBody = cands.length ? rows : `<div class="queue-empty">Nothing queued.</div>`;
  const queue = `<div class="queue">
    <div class="queue-head"><span class="t">Review inbox</span><span class="n">${cands.length} left</span></div>
    <div class="queue-list">${queueBody}</div>
    <div class="queue-foot"><span class="mono"><b>j/k</b> move</span><span class="mono"><b>a</b> approve</span><span class="mono"><b>r</b> reject</span></div>
  </div>`;

  let focus;
  const cur = cands[state.queueIndex];
  if (!cur) {
    focus = `<div class="focus"><div class="caught-up">
      <div class="caught-circle">✓</div>
      <div class="caught-title">All caught up</div>
      <div class="caught-sub">${state.reviewed} reviewed · nothing left to triage.</div>
    </div></div>`;
  } else {
    const body = `<div class="cand-body md-body">${cur.body || ''}</div>`;
    const supersedes = cur.supersedes ? `
      <div class="supersedes"><div class="lbl">Approving this retires ↓</div>
        <div class="row"><span class="id">${esc(cur.supersedes.id)}</span><span class="title">${esc(cur.supersedes.title)}</span></div></div>` : '';
    focus = `<div class="focus"><div class="focus-pad">
      <div class="cand">
        <div class="cand-head"><span class="cand-draft">PROPOSED LESSON</span><span class="cand-guess">From session evidence · you decide</span><span class="spacer"></span><span class="cand-id">${esc(cur.id)}</span></div>
        <h2 class="cand-title">${esc(cur.title)}</h2>
        <div class="cand-summary">${esc(cur.summary)}</div>
        <div class="cand-meta">
          <span class="type-pill">${typeIcon(cur.type)} ${esc(cur.type)}</span>
          ${scopeChips(cur.scopeLabels, true)}
          <span class="cand-obs">observations: <b>${esc(cur.observations)}</b></span>
        </div>
        ${body}${supersedes}
        <div class="prov">
          <div class="prov-title">Provenance</div>
          <div class="prov-grid">
            <div><div class="prov-k">source</div><span class="prov-tag">${esc(cur.source)}</span></div>
            <div><div class="prov-k">model</div><span class="prov-v">${esc(cur.model)}</span></div>
            <div><div class="prov-k">created</div><span class="prov-v">${esc(cur.created)}</span></div>
          </div>
          <div class="prov-ev-lbl">evidence from transcript</div>
          <div class="prov-ev">${esc(cur.evidence)}</div>
        </div>
      </div>
      <div class="actionbar">
        <button class="act-approve" data-act="approve">Approve <span class="act-key">a</span></button>
        <button class="act-reject" data-act="reject">Reject <span class="act-key">r</span></button>
        <button class="act-skip" data-act="skip">Skip <span class="act-key">s</span></button>
        <div class="spacer"></div>
        <button class="act-edit" data-act="editCandidate">Open editor</button>
      </div>
    </div></div>`;
  }
  return `<div class="rv">${queue}${focus}</div>`;
}

// ---------- knowledge base ----------
// kbFiltered applies the type/status facets and the search box to the card
// list. Search matches id, title, summary, key, scopes, and trigger keywords —
// a fast local filter over the corpus metadata (semantic search over bodies is
// a future server-backed addition).
function kbFiltered() {
  const cards = state.cards || [];
  const anyType = Object.values(state.typeFilter).some(Boolean);
  const anyStatus = Object.values(state.statusFilter).some(Boolean);
  const q = (state.kbSearch || '').trim().toLowerCase();
  return cards.filter(c => {
    if (state.staleFilter && !c.stale) return false;
    if (anyType && !state.typeFilter[c.type]) return false;
    if (anyStatus && !state.statusFilter[c.status]) return false;
    if (!q) return true;
    const hay = [c.id, c.title, c.summary, c.key, ...(c.scopeLabels || []), ...(c.triggers || [])].join(' ').toLowerCase();
    return hay.includes(q);
  });
}
function kbRowHtml(c) {
  const sel = c.id === state.kbId;
  const spark = (c.spark || []).map(h => `<div class="b" style="height:${h}%"></div>`).join('');
  const base = c.baseline ? `<span class="base-tag">BASE</span>` : '';
  return `<button class="kb-row ${sel ? 'sel' : ''}" data-act="selCard:${esc(c.id)}">
      <div class="kb-row-top">${typeIcon(c.type)}<span class="q-shortid">${esc(c.id)}</span>
        <span class="dot statusdot-${c.status}"></span>${base}<span class="spacer"></span>
        <span class="kb-injected">↑${esc(c.injected)}</span><div class="kb-spark">${spark}</div></div>
      <div class="kb-title ${c.status === 'retired' ? 'retired' : ''}">${esc(c.title)}</div>
      <div class="kb-scopes">${scopeChips(c.scopeLabels)}</div>
    </button>`;
}
function kbRowsHtml(list) {
  if (!list.length) return `<div class="kb-empty">${state.kbSearch ? 'No cards match your search.' : 'No cards match these filters.'}</div>`;
  return list.map(kbRowHtml).join('');
}
// updateKbList re-renders only the list rows + count (on search keystroke), so
// the search input keeps focus and the cursor position.
function updateKbList() {
  const list = kbFiltered();
  const wrap = document.querySelector('.kb-rows');
  if (wrap) wrap.innerHTML = kbRowsHtml(list);
  const count = document.getElementById('kb-count');
  if (count) count.textContent = list.length;
}

function knowledgeModeBar() {
  return `<div class="kb-modebar">
    <div class="segmented kb-modes" aria-label="Knowledge view">
      <button class="seg ${state.kbMode === 'cards' ? 'active' : ''}" data-act="kbMode:cards">Cards</button>
      <button class="seg ${state.kbMode === 'analytics' ? 'active' : ''}" data-act="kbMode:analytics">Analytics</button>
    </div>
    <span>${state.kbMode === 'analytics' ? 'See which knowledge Culi actually delivers' : 'Browse, inspect, and manage reusable knowledge'}</span>
  </div>`;
}

function screenKnowledgeAnalytics() {
  const a = state.analytics;
  const summary = (a && a.summary) || {};
  const rows = ((a && a.cards) || []).slice(0, 20).map((c, i) => {
    const agentSplit = (c.harnesses || []).map(h => `<span class="pulse-agent-dot harness-${harnessClass(h.code)}"></span>${esc(h.label)} ${fmtCount(h.sessions)}`).join('<span class="pulse-split">·</span>');
    const title = c.available
      ? `<button class="pulse-table-link" data-act="analyticsCard:${esc(c.short)}">${esc(c.title)}</button><span class="mono pulse-id">${esc(c.id)}</span>`
      : `<span class="pulse-table-link unavailable">${esc(c.title)}</span><span class="pulse-removed">removed</span>`;
    const verdict = c.bucket
      ? `${effBadge(c.bucket, c.pullRate ? `pull rate ${c.pullRate} — expansions + references per injection` : '')}${c.pullRate ? `<span class="eff-rate mono">${esc(c.pullRate)}</span>` : ''}`
      : '—';
    return `<tr>
      <td class="mono pulse-table-rank">${i + 1}</td><td>${title}</td>
      <td class="pulse-table-num">${fmtCount(c.sessions)}</td><td class="pulse-table-num">${fmtCount(c.deliveries)}</td>
      <td class="pulse-table-num">${fmtCount(c.tokens)}</td><td class="pulse-verdict">${verdict}</td>
      <td class="pulse-agents">${agentSplit || '—'}</td><td>${esc(c.lastUsed || '—')}</td>
    </tr>`;
  }).join('');
  const verdicts = ['helpful', 'noisy', 'expensive', 'uncertain']
    .map(b => `<span class="pulse-verdict-item">${effBadge(b)}<b>${fmtCount(summary[b])}</b></span>`).join('');
  const content = `<div class="pulse-kpis">
      <div><span>Cards used</span><b>${fmtCount(summary.activeCards)}</b></div>
      <div><span>Sessions</span><b>${fmtCount(summary.sessions)}</b></div>
      <div><span>Deliveries</span><b>${fmtCount(summary.deliveries)}</b></div>
      <div><span>Tokens delivered</span><b>${fmtCount(summary.tokens)}</b></div>
      <div><span>Cross-agent cards</span><b>${fmtCount(summary.crossHarnessCards)}</b></div>
    </div>
    <div class="pulse-verdicts" title="Verdicts combine lifetime pull/downvote signals with 7-day token cost; they don't change with the filters above. A full card pushed into the prompt can help without being expanded — silence alone never marks a card noisy.">
      <span class="pulse-verdicts-label">Card verdicts</span>${verdicts}
    </div>
    <section class="pulse-detail-card card">
      <div class="pulse-section-head"><div><h3>Most useful cards</h3><p>Distinct sessions are the strongest signal; repeated deliveries in one session do not inflate the ranking.</p></div><span>${esc((a && a.range) || 'Last 7 days')}</span></div>
      ${analyticsChart(a, 10)}
    </section>
    <section class="pulse-table-card card">
      <div class="pulse-section-head"><div><h3>Card activity</h3><p>Up to 20 cards in the current agent and repository view.</p></div></div>
      ${rows ? `<div class="pulse-table-wrap"><table class="pulse-table"><thead><tr><th>#</th><th>Card</th><th>Sessions</th><th>Deliveries</th><th>Tokens</th><th>Verdict</th><th>Agent split</th><th>Last used</th></tr></thead><tbody>${rows}</tbody></table></div>` : analyticsChart(a, 20)}
    </section>`;
  return `<div class="pulse-page">
    <div class="pulse-page-head"><div><div class="pulse-eyebrow">Knowledge Pulse</div><h2>What Culi is using</h2><p>Real delivery activity across Claude Code and Codex. Filters are shared with Home.</p></div>${analyticsFilters(a)}</div>
    ${analyticsStatus(a, content)}
  </div>`;
}

function screenKb() {
  return `<div class="kb-shell">${knowledgeModeBar()}${state.kbMode === 'analytics' ? screenKnowledgeAnalytics() : screenKbCards()}</div>`;
}

function screenKbCards() {
  const filtered = kbFiltered();
  if (!state.kbId && filtered.length) state.kbId = filtered[0].id;

  const cards = state.cards || [];
  const tCount = {}, sCount = {};
  cards.forEach(c => { tCount[c.type] = (tCount[c.type] || 0) + 1; sCount[c.status] = (sCount[c.status] || 0) + 1; });

  const typeFacets = Object.keys(TYPE).map(t => {
    const on = !!state.typeFilter[t];
    const [, color] = TYPE[t];
    const style = on ? `background:${alpha(color, 0.14)};border-color:${alpha(color, 0.45)};color:${color};` : '';
    return `<button class="tfacet ${on ? 'on' : ''}" style="${style}" data-act="facetType:${t}">
      ${typeIcon(t)}<span class="tfacet-label">${t}</span><span class="tfacet-count">${tCount[t] || 0}</span></button>`;
  }).join('');
  const statusFacets = Object.keys(STATUS).map(t => {
    const on = !!state.statusFilter[t];
    const color = STATUS_COLOR[t];
    const style = on ? `background:${alpha(color, 0.14)};border-color:${alpha(color, 0.45)};color:${color};` : '';
    return `<button class="tfacet ${on ? 'on' : ''}" style="${style}" data-act="facetStatus:${t}">
      <span class="dot statusdot-${t}"></span><span class="tfacet-label">${t}</span><span class="tfacet-count">${sCount[t] || 0}</span></button>`;
  }).join('');

  const staleCount = cards.filter(c => c.stale).length;
  const staleColor = COLOR.primary;
  const staleStyle = state.staleFilter ? `background:${alpha(staleColor, 0.14)};border-color:${alpha(staleColor, 0.45)};color:${staleColor};` : '';
  const staleFacet = `<button class="tfacet ${state.staleFilter ? 'on' : ''}" style="${staleStyle}" data-act="facetStale">
    <span class="dot" style="background:${staleColor}"></span><span class="tfacet-label">stale</span><span class="tfacet-count">${staleCount}</span></button>`;

  const st = state.status || {};
  const foot = st.ollamaOffline
    ? `<div class="kb-foot"><span class="dot"></span> keyword-only — <b>ollama offline, semantic search degraded</b></div>`
    : '';

  return `<div class="kb">
    <div class="kb-list">
      <div class="kb-filter">
        <div class="kb-search-wrap">
          <span class="kb-search-icon">${uiIcon('search')}</span>
          <input id="kb-search" class="kb-search" placeholder="Search cards…  ( / )" value="${esc(state.kbSearch)}" autocomplete="off" spellcheck="false" />
          <span class="kb-search-count mono" id="kb-count">${filtered.length}</span>
        </div>
        <div class="facet-group-label">Type</div>
        <div class="kb-facets">${typeFacets}</div>
        <div class="facet-group-label">Status</div>
        <div class="kb-facets status">${statusFacets}</div>
        <div class="facet-group-label">Freshness</div>
        <div class="kb-facets">${staleFacet}</div>
      </div>
      <div class="kb-rows">${kbRowsHtml(filtered)}</div>
      ${foot}
    </div>
    ${kbDetail(filtered)}
  </div>`;
}
function kbEditPanel(c) {
  const ro = c.editable === false;
  return `<div class="edit">
    <div class="edit-head"><span class="t">Edit card</span>${ro ? '<span class="cand-id">read-only · hand-authored</span>' : ''}</div>
    <label>Title</label><input id="kbedit-title" value="${esc(c.title)}" />
    <div class="edit-2col">
      <div><label>Scope <span class="faint">(space-separated)</span></label><input id="kbedit-scope" class="mono" value="${esc((c.scopeLabels || []).join(' '))}" /></div>
      <div><label>Key</label><input id="kbedit-key" class="mono" value="${esc(c.key || '')}" /></div>
    </div>
    <label>Trigger keywords <span class="faint">(comma-separated)</span></label><input id="kbedit-keywords" class="mono" value="${esc((c.keywords || []).join(', '))}" />
    <div class="edit-btns"><button class="btn-teal" data-act="kbSaveEdit">Save changes</button><button class="btn-outline" data-act="kbCancelEdit">Cancel</button></div>
  </div>`;
}
function kbDetail(filtered) {
  const cards = state.cards || [];
  const c = state.cardDetail[state.kbId] || cards.find(x => x.id === state.kbId) || filtered[0] || cards[0];
  if (!c) return `<div class="kb-detail"><div class="kb-detail-pad skeleton">Select a card.</div></div>`;
  const [statusLabel] = STATUS[c.status] || STATUS.confirmed;
  const base = c.baseline ? `<span class="base-tag base-tag-lg">baseline · always in session-start</span>` : '';
  const key = c.key ? `<span class="kb-d-key">key: ${esc(c.key)}</span>` : '';
  const usage = (c.usage || []).map(u =>
    `<div class="usage-tile"><div class="usage-label">${esc(u.label)}</div><div class="usage-val" style="color:${u.color}">${esc(u.value)}</div></div>`).join('');
  const trig = (c.triggers || []).map(t => `<span class="trig-chip">${esc(t)}</span>`).join('');
  const hist = (c.history || []).map(h =>
    `<div class="hist-row"><div class="hist-rail"><span class="hist-dot" style="background:${h.dot}"></span><span class="hist-line"></span></div>
      <div class="hist-body"><div class="hist-action"><b style="color:${h.dot}">${esc(h.action)}</b> ${esc(h.detail)}</div>
        <div class="hist-meta">${esc(h.date)} · ${esc(h.sha)}</div></div>
      ${destrBtn('revert:' + h.sha, 'Revert', 'hist-revert')}</div>`).join('');
  const retired = c.status === 'retired';
  return `<div class="kb-detail"><div class="kb-detail-pad">
    <div class="kb-d-head">
      <span class="type-pill">${typeIcon(c.type)} ${esc(c.type)}</span>
      <span class="status-badge status-${c.status}">${statusLabel}</span>${base}
      ${c.eff ? effBadge(c.eff.bucket, c.eff.note) : ''}
      <span class="spacer"></span><span class="cand-id">${esc(c.id)}</span>
    </div>
    <h2 class="kb-d-title ${retired ? 'retired' : ''}">${esc(c.title)}</h2>
    <div class="kb-d-scopes">${scopeChips(c.scopeLabels, true)}${key}</div>
    ${state.kbEditing ? kbEditPanel(c) : `<div class="kb-body md-body">${c.body || ''}</div>`}
    <div class="usage">${usage}</div>
    <div class="kb-panel"><div class="kb-panel-head"><span class="section-label">Triggers &amp; aliases</span><a data-act="cardEdit">Edit</a></div><div class="trig-chips">${trig}</div></div>
    <div class="kb-panel">
      <div class="prov-title">Provenance</div>
      <div class="prov-grid">
        <div><div class="prov-k">source</div><span class="prov-tag">${esc(c.source)}</span></div>
        <div><div class="prov-k">merged from</div><span class="prov-v">${esc(c.mergedFrom || '—')}</span></div>
        <div><div class="prov-k">model</div><span class="prov-v">${esc(c.model || '—')}</span></div>
        <div><div class="prov-k">source_hash</div><span class="prov-v" style="color:var(--faint)">${esc(c.hash || '—')}</span></div>
      </div>
    </div>
    <div class="kb-panel history"><div class="prov-title">History <span style="color:var(--faint2);font-weight:400;text-transform:none;letter-spacing:0">· git audit trail</span></div>${hist}</div>
    <div class="kb-actionbar">
      <button class="btn btn-outline" data-act="cardEdit">Edit</button>
      <button class="btn btn-outline" data-act="cardDown">Down</button>
      <div class="spacer"></div>
      ${destrBtn('cardRetire', 'Retire', 'btn btn-amber-outline')}
      ${destrBtn('cardRemove', 'Remove', 'btn btn-red-outline')}
    </div>
  </div></div>`;
}

// ---------- activity ----------
// granPill renders one granularity as a color-coded chip — the same body/
// summary/hook color language as the Overview granularity bar, so a glance
// tells you how much of each card Claude actually saw.
function granPill(g) {
  const t = String(g || '').toLowerCase().replace(/[^a-z]/g, '');
  return `<span class="gran-pill gran-${t}"><i></i>${esc(g)}</span>`;
}
function granPills(str) {
  return String(str || '').split('+').filter(Boolean).map(granPill).join('');
}
// repoChip renders the session's repo as an unmistakable labelled chip
// (REPO · name), or a muted "no repo logged" chip when the working dir wasn't
// recorded (pre-repo-tracking sessions).
function repoChip(s) {
  if (s.repo) {
    return `<span class="sess-repo" title="${esc(s.repoPath || s.repo)}">
      <span class="repo-tag">repo</span><span class="repo-name">${esc(s.repo)}</span></span>`;
  }
  return `<span class="sess-repo unknown" title="Working directory wasn't recorded for this session (logged before repo tracking).">
    <span class="repo-tag">repo</span><span class="repo-name">not logged</span></span>`;
}

// injCardRow renders one injected card inside an expanded event: the exact
// card id that was applied, its granularity, and its token cost. When the card
// still exists in the corpus (has a short id), the row deep-links to its KB
// detail so the user can see *why* it was injected.
function injCardRow(c) {
  const link = c.short ? `data-act="injCard:${esc(c.short)}"` : '';
  const tail = c.short
    ? '<span class="inj-go" aria-hidden="true">→</span>'
    : '<span class="inj-gone" title="no longer in the knowledge base">removed</span>';
  return `<div class="inj-card ${c.short ? 'link' : ''}" ${link} ${c.short ? 'role="button" tabindex="0"' : ''}
      title="${c.short ? 'Open ' + esc(c.id) + ' in the Knowledge Base' : ''}">
    <span class="inj-dot"></span>
    <span class="inj-card-id mono">${esc(c.id)}</span>
    <span class="spacer"></span>
    ${granPill(c.gran)}
    <span class="inj-card-tok mono">${esc(c.tok)} tok</span>
    ${tail}
  </div>`;
}
// harnessLabel maps a harness code to its human label using the option list the
// server sent (labels are authored in the harness package, never here).
function harnessLabel(code) {
  if (!code) return '';
  const opt = (state.actHarnesses || []).find(h => h.code === code);
  return opt ? opt.label : code;
}
function activityFilters() {
  const repoOpts = ['<option value="">All repos</option>'].concat(
    (state.actRepos || []).map(r => `<option value="${esc(r)}"${state.actRepo === r ? ' selected' : ''}>${esc(r)}</option>`)
  ).join('');
  // Only surface the harness filter once more than one agent has been seen —
  // single-agent users never meet a control they cannot use.
  let harnessSel = '';
  if ((state.actHarnesses || []).length > 1) {
    const opts = ['<option value="">All agents</option>'].concat(
      state.actHarnesses.map(h => `<option value="${esc(h.code)}"${state.actHarness === h.code ? ' selected' : ''}>${esc(h.label)}</option>`)
    ).join('');
    harnessSel = `<select class="act-sel mono" data-change="actHarness" title="Filter by coding agent">${opts}</select>`;
  }
  const dates = [['all', 'All'], ['today', 'Today'], ['7d', '7d'], ['30d', '30d']];
  const dateSeg = dates.map(([v, l]) =>
    `<button class="seg ${state.actSince === v ? 'active' : ''}" data-act="actSince:${v}">${esc(l)}</button>`).join('');
  return `<div class="act-filters">
    <select class="act-sel mono" data-change="actRepo" title="Filter by repository">${repoOpts}</select>
    ${harnessSel}
    <div class="segmented sm" title="Filter by time">${dateSeg}</div>
  </div>`;
}

function screenActivity() {
  const inj = state.actTab === 'inj';
  const seg = `<div class="act-top">
    <div class="segmented">
      <button class="seg ${inj ? 'active' : ''}" data-act="actTab:inj">Injections</button>
      <button class="seg ${!inj ? 'active' : ''}" data-act="actTab:learn">Learning runs</button>
    </div>
    ${inj ? `<div class="gran-key">
      <span class="gran-key-lbl">granularity</span>
      <span class="gran-pill gran-body"><i></i>body</span><span class="gran-key-note">full card</span>
      <span class="gran-pill gran-summary"><i></i>summary</span><span class="gran-key-note">~60 tok</span>
      <span class="gran-pill gran-hook"><i></i>hook</span><span class="gran-key-note">one-liner</span>
    </div>` : ''}
  </div>${inj ? activityFilters() : ''}`;

  let body;
  if (inj) {
    const filtering = !!state.actRepo || !!state.actHarness || (state.actSince && state.actSince !== 'all');
    const sessions = (state.sessions || []).map((s, si) => {
      const nEvents = (s.events || []).length;
      const events = (s.events || []).map((e, ei) => {
        const key = si + ':' + ei;
        const open = !!state.injOpen[key];
        const n = (e.list || []).length;
        const detail = open ? `<div class="sess-ev-detail">${(e.list || []).map(injCardRow).join('')}</div>` : '';
        return `<div class="sess-ev-wrap ${open ? 'open' : ''}">
          <div class="sess-ev ${open ? 'open' : ''}" data-act="injToggle:${key}" role="button" tabindex="0"
            title="${open ? 'Hide' : 'Show'} the ${n} card${n === 1 ? '' : 's'} injected here">
            <span class="sess-ev-caret">${open ? '▾' : '▸'}</span>
            <span class="sess-ev-at mono">${esc(e.at)}</span>
            <span class="ev-badge ev-${e.ev}">${esc(e.ev)}</span>
            <span class="sess-ev-cards">${esc(e.cards)}</span>
            <span class="sess-ev-grans">${granPills(e.gran)}</span>
            <span class="sess-ev-tok mono">${esc(e.tok)}</span>
          </div>${detail}</div>`;
      }).join('');
      const hLabel = harnessLabel(s.harness);
      const hBadge = hLabel
        ? `<span class="harness-badge harness-${esc(s.harness)}" title="Injected into ${esc(hLabel)}">${esc(hLabel)}</span>`
        : '';
      const idTitle = hLabel ? esc(hLabel) + ' conversation (session id)' : 'conversation (session id)';
      return `<div class="sess">
        <div class="sess-head">
          ${repoChip(s)}
          ${hBadge}
          <span class="sess-id mono" title="${idTitle}">${esc(s.id)}</span>
          <span class="sess-time">${esc(s.time)}</span>
          <span class="spacer"></span>
          <span class="sess-nev">${nEvents} event${nEvents === 1 ? '' : 's'}</span>
          <span class="sess-tok mono">${esc(s.tokens)} tok</span>
        </div>
        <div class="sess-box">${events}<div class="sess-foot">↳ click any row to see exactly which cards the agent was shown</div></div>
      </div>`;
    }).join('');
    body = sessions || (filtering
      ? `<div class="act-empty"><div class="act-empty-ic">◷</div>
        <div class="act-empty-t">No injections match this filter</div>
        <div class="act-empty-s">Widen the repo or date range to see more recent activity.</div></div>`
      : `<div class="act-empty"><div class="act-empty-ic">◷</div>
        <div class="act-empty-t">No injections logged yet</div>
        <div class="act-empty-s">Start a Claude or Codex session and Culi's injections will appear here, grouped by conversation.</div></div>`);
  } else {
    const runs = (state.runs || []).map(r =>
      `<div class="run ${r.failed ? 'failed' : ''}"><span class="dot" style="background:${r.dot}"></span>
        <span class="run-date mono">${esc(r.date)}</span>
        <span class="run-detail">mined <b>${esc(r.mined)}</b> · clean <b>${esc(r.clean)}</b> · <b class="amber">${esc(r.candidates)}</b> candidates · ${esc(r.patterns)} patterns</span>
        <span class="run-spend mono">${esc(r.spend)}</span></div>`).join('');
    body = runs || `<div class="act-empty"><div class="act-empty-ic">◷</div>
      <div class="act-empty-t">No learning runs yet</div>
      <div class="act-empty-s">culi mines your transcripts after sessions end; runs and spend land here.</div></div>`;
  }
  return `<div class="act">${seg}${body}</div>`;
}

// ---------- reconcile (import / merge / apply) ----------
// A guided top-to-bottom flow: Scan the repos → Merge the divergent copies
// (a paid LLM job, run in the background with a progress bar) → review and
// Apply the staged cards, and place the leftover CLAUDE.md residuals.
function recClassChip(cls) {
  const m = { diverged: ['diverged', COLOR.warning], unique: ['unique', COLOR.primary], identical: ['identical', COLOR.neutral], superset: ['superset', COLOR.neutral] };
  const [label, color] = m[cls] || m.identical;
  return `<span class="rec-chip" style="background:${alpha(color, 0.14)};color:${color};">${label}</span>`;
}
function recBtn(act, label, cls, opts) {
  const o = opts || {};
  const dis = state.recBusy || o.disabled;
  return `<button class="${cls}${dis ? ' is-disabled' : ''}" data-act="${esc(act)}"${dis ? ' disabled' : ''}>${state.recBusy === act ? '…' : esc(label)}</button>`;
}

function recStepScan(im) {
  const sc = im.scan || {};
  const repos = (sc.repos || []).map(r =>
    `<div class="rec-repo"><span class="rec-repo-name">${esc(r.name)}</span>
      <span class="rec-repo-meta">${r.agents}a · ${r.skills}s${r.claudeMd ? ' · CLAUDE.md' : ''}</span></div>`).join('');
  const clusters = (sc.clusters || []).slice(0, 40).map(c =>
    `<div class="rec-cluster"><span class="rec-cluster-kind mono">${esc(c.kind)}</span>
      <span class="rec-cluster-name">${esc(c.name)}</span>${recClassChip(c.class)}
      <span class="spacer"></span><span class="rec-cluster-repos">${c.repos} repos</span></div>`).join('');
  const summary = sc.scanned
    ? `<div class="rec-scan-summary"><b class="warn">${sc.diverged}</b> diverged <span>·</span> <b>${sc.unique}</b> unique <span>·</span> <b>${sc.identical}</b> identical <span class="rec-when">scanned ${esc(sc.at || '')}</span></div>`
    : `<div class="rec-empty">No scan yet. Scan inventories every watched repo and finds rules/skills that have drifted apart.</div>`;
  return `<section class="rec-step">
    <div class="rec-step-head"><span class="rec-step-n">1</span><div><h3>Scan repositories</h3><p>Inventory watched repos and cluster shared agents & skills by how far their copies have drifted. Fast, read-only, no LLM.</p></div>
      ${recBtn('recScan', sc.scanned ? 'Re-scan' : 'Scan now', 'btn btn-teal')}</div>
    ${summary}
    ${sc.scanned ? `<div class="rec-repos">${repos}</div>${clusters ? `<div class="rec-clusters">${clusters}</div>` : ''}` : ''}
  </section>`;
}

function recStepMerge(im) {
  const m = im.merge || {};
  const sc = im.scan || {};
  if (!sc.scanned) return '';
  let body;
  if (m.running) {
    const pct = m.total > 0 ? Math.min(100, Math.round((m.done / m.total) * 100)) : 0;
    body = `<div class="rec-merge-running">
      <div class="rec-progress"><div class="rec-progress-fill" style="width:${pct}%"></div></div>
      <div class="rec-merge-status"><span class="rec-spin"></span> Merging… <b>${m.done}</b>/${m.total} units · this runs in the background, you can leave this page</div></div>`;
  } else if (m.err) {
    body = `<div class="rec-merge-err">Last merge failed: <span class="mono">${esc(m.err)}</span><div>Re-run to resume from where it stopped — finished units are kept.</div></div>`;
  } else if (!m.hasWork) {
    body = `<div class="rec-empty">Nothing to merge — no diverged clusters or CLAUDE.md files in the last scan.</div>`;
  } else if (state.recBusy === 'recMergeArmed') {
    body = `<div class="rec-merge-confirm">⚠ This starts a real LLM merge — roughly <b>a minute per divergent unit</b> and it spends model budget (or uses your CLI subscription). ${recBtn('recMerge', 'Yes, start merge', 'btn btn-teal')} <button class="btn btn-outline" data-act="recMergeCancel">Cancel</button></div>`;
  } else {
    body = `<div class="rec-merge-idle"><span>${sc.diverged} diverged + CLAUDE.md decomposition to synthesize.</span>${recBtn('recMergeArm', 'Merge divergent copies', 'btn btn-teal')}</div>`;
  }
  return `<section class="rec-step">
    <div class="rec-step-head"><span class="rec-step-n">2</span><div><h3>Merge divergent copies</h3><p>Synthesize each cluster's variants into one canonical card, and decompose each repo's CLAUDE.md. Output is staged for review — nothing touches your store yet.</p></div></div>
    ${body}
  </section>`;
}

function recCardDiff(c, key) {
  const open = !!state.recOpen[key];
  const delta = c.conflict ? `<span class="rec-delta"><span class="rec-add">+${c.added}</span> <span class="rec-del">−${c.removed}</span></span>` : `<span class="rec-new">new</span>`;
  const detail = open ? `<div class="rec-diff">
    ${c.conflict ? `<div class="rec-diff-col"><div class="rec-diff-lbl">Current (live)</div><pre>${esc(c.live || '')}</pre></div>` : ''}
    <div class="rec-diff-col"><div class="rec-diff-lbl">${c.conflict ? 'New (staged)' : 'Staged'}</div><pre>${esc(c.staged || '')}</pre></div>
  </div>` : '';
  return `<div class="rec-file-wrap ${open ? 'open' : ''}">
    <div class="rec-file" data-act="recToggle:${esc(key)}" role="button" tabindex="0">
      <span class="rec-caret">${open ? '▾' : '▸'}</span><span class="rec-file-rel mono">${esc(c.rel)}</span>
      <span class="spacer"></span>${delta}</div>${detail}</div>`;
}

function recResidual(r, key) {
  const open = !!state.recOpen[key];
  const target = r.missing
    ? `<span class="rec-res-missing" title="No matching watched repo / CLAUDE.md found">no target repo</span>`
    : `<span class="rec-res-target mono" title="${esc(r.repoPath)}">${esc(r.repo)}/CLAUDE.md</span>`;
  const detail = open ? `<div class="rec-diff">
    ${!r.missing ? `<div class="rec-diff-col"><div class="rec-diff-lbl">Current repo CLAUDE.md</div><pre>${esc(r.live || '')}</pre></div>` : ''}
    <div class="rec-diff-col"><div class="rec-diff-lbl">Staged replacement</div><pre>${esc(r.staged || '')}</pre></div>
  </div><div class="rec-res-note">Residuals aren't applied automatically — review the diff and hand-place the staged version into the repo's CLAUDE.md so any edits you've made since aren't lost.</div>` : '';
  return `<div class="rec-file-wrap ${open ? 'open' : ''}">
    <div class="rec-file" data-act="recToggle:${esc(key)}" role="button" tabindex="0">
      <span class="rec-caret">${open ? '▾' : '▸'}</span><span class="rec-file-rel mono">${esc(r.repo)}</span>
      <span class="spacer"></span>${target}</div>${detail}</div>`;
}

function recStepApply(im) {
  const st = im.staging || {};
  if (!st.present) {
    return `<section class="rec-step">
      <div class="rec-step-head"><span class="rec-step-n">3</span><div><h3>Review & apply</h3><p>After a merge, staged cards appear here to review before they land in your knowledge store.</p></div></div>
      <div class="rec-empty">Nothing staged. Run a merge first.</div>
    </section>`;
  }
  const ready = (st.ready || []).map((c, i) => recCardDiff(c, 'r' + i)).join('');
  const conflicts = (st.conflicts || []).map((c, i) => recCardDiff(c, 'c' + i)).join('');
  const residuals = (st.residuals || []).map((r, i) => recResidual(r, 'd' + i)).join('');
  const nReady = (st.ready || []).length, nConf = (st.conflicts || []).length, nRes = (st.residuals || []).length;
  const applyLabel = state.recForce ? `Apply ${nReady + nConf} (overwrite ${nConf})` : (nReady ? `Apply ${nReady} ready` : 'Apply');
  return `<section class="rec-step">
    <div class="rec-step-head"><span class="rec-step-n">3</span><div><h3>Review & apply</h3><p>Ready cards land silently; conflicts differ from a live card and only overwrite when you allow it. Every apply is one git commit — fully revertible.</p></div></div>
    <div class="rec-apply-summary"><b>${nReady}</b> ready <span>·</span> <b class="warn">${nConf}</b> conflict${nConf === 1 ? '' : 's'} <span>·</span> <b>${nRes}</b> residual${nRes === 1 ? '' : 's'}</div>
    ${nReady ? `<div class="rec-group-label">Ready to apply</div><div class="rec-files">${ready}</div>` : ''}
    ${nConf ? `<div class="rec-group-label">Conflicts <span class="faint">— differ from live cards</span></div><div class="rec-files">${conflicts}</div>` : ''}
    ${nRes ? `<div class="rec-group-label">Residual CLAUDE.md <span class="faint">— place by hand</span></div><div class="rec-files">${residuals}</div>` : ''}
    <div class="rec-apply-bar">
      <label class="rec-force ${nConf ? '' : 'faint'}"><input type="checkbox" ${state.recForce ? 'checked' : ''} ${nConf ? '' : 'disabled'} data-act="recForce" /> overwrite ${nConf} conflict${nConf === 1 ? '' : 's'}</label>
      <div class="spacer"></div>
      ${destrBtn('recDiscard', 'Discard staging', 'btn btn-red-outline')}
      ${recBtn('recApply', applyLabel, 'btn btn-teal', { disabled: !nReady && !(state.recForce && nConf) })}
    </div>
  </section>`;
}

function screenReconcile() {
  const im = state.reconcile || SEED.import;
  return `<div class="rec">
    <div class="rec-intro">Culi keeps one canonical set of rules, skills, and agents. When the same card drifts across repos, reconcile folds the copies back together — you review every change before it lands.</div>
    ${recStepScan(im)}
    ${recStepMerge(im)}
    ${recStepApply(im)}
  </div>`;
}

// ---------- settings ----------
function settingValue(key, fallback) {
  const draft = state.settingsDraft;
  return draft && Object.prototype.hasOwnProperty.call(draft, key) ? draft[key] : (fallback == null ? '' : fallback);
}

function captureSettingsDraft() {
  const draft = Object.assign({}, state.settingsDraft || {});
  document.querySelectorAll('.set-input').forEach(i => { draft[i.dataset.key] = i.value; });
  state.settingsDraft = draft;
  return draft;
}

function setLearningProvider(code) {
  const s = state.settings || SEED.settings;
  const learning = s.learning || {};
  const selected = (learning.backends || []).find(b => b.code === code);
  if (!selected) return;
  const draft = captureSettingsDraft();
  draft.provider = code;
  draft.cheap_model = selected.cheapModel || draft.cheap_model || '';
  draft.strong_model = selected.strongModel || draft.strong_model || '';
  renderScreen();
}

function learningCredential(provider, learning) {
  if (provider === 'openai') return `<label class="learning-field learning-field-wide">
    <span>API key file <em>optional when OPENAI_API_KEY is set</em></span>
    <input class="set-input" data-key="openai_api_key_file" value="${esc(settingValue('openai_api_key_file', learning.openaiApiKeyFile))}" placeholder="~/.culi/secrets/openai.key" autocomplete="off" />
  </label>`;
  if (provider === 'anthropic') return `<label class="learning-field learning-field-wide">
    <span>API key file <em>optional when ANTHROPIC_API_KEY is set</em></span>
    <input class="set-input" data-key="anthropic_api_key_file" value="${esc(settingValue('anthropic_api_key_file', learning.anthropicApiKeyFile))}" placeholder="~/.culi/secrets/anthropic.key" autocomplete="off" />
  </label>`;
  if (provider === 'claude-cli') return `<label class="learning-field learning-field-wide">
    <span>Headless OAuth token file <em>optional for interactive sessions</em></span>
    <input class="set-input" data-key="oauth_token_file" value="${esc(settingValue('oauth_token_file', learning.oauthTokenFile))}" placeholder="~/.claude-tokens/account.token" autocomplete="off" />
  </label>`;
  if (provider === 'codex-cli') return `<div class="learning-auth-note learning-field-wide"><span class="status-dot ready"></span>
    Culi uses your existing Codex login. If setup is required, run <code>codex login</code> in a terminal.</div>`;
  if (provider === 'ollama') return `<div class="learning-auth-note learning-field-wide"><span class="status-dot ready"></span>
    Generation stays local. Make sure your selected models are pulled in Ollama.</div>`;
  if (provider === 'auto') return `<div class="learning-auth-note learning-field-wide"><span class="status-dot auto"></span>
    Auto tries configured API keys first, then signed-in Claude and Codex terminals. Existing credential paths remain unchanged.</div>`;
  return `<div class="learning-auth-note learning-field-wide"><span class="status-dot off"></span>No transcript mining calls will run.</div>`;
}

function screenSettings() {
  const s = state.settings || SEED.settings;
  const learning = s.learning || { provider: 'auto', cheapModel: '', strongModel: '', backends: [] };
  const provider = settingValue('provider', learning.provider || 'auto');
  const selected = (learning.backends || []).find(b => b.code === provider) || (learning.backends || [])[0] || {};
  const backends = (learning.backends || []).map(b => `<button type="button" class="backend-option ${b.code === provider ? 'selected' : ''}" data-act="setProvider:${esc(b.code)}" aria-pressed="${b.code === provider}">
    <span class="backend-mark mark-${esc(b.code)}">${esc(b.mark)}</span>
    <span class="backend-copy"><span class="backend-name">${esc(b.label)}</span><span class="backend-desc">${esc(b.description)}</span></span>
    <span class="backend-meta"><span class="backend-status tone-${esc(b.tone)}"><i></i>${esc(b.status)}</span><span>${esc(b.billing)}</span></span>
  </button>`).join('');
  const groups = (s.groups || []).map(g => {
    const rows = g.items.map(i => {
      const control = i.key === 'repos'
        ? `<button class="btn btn-outline set-repos-btn" data-act="openRepos">Manage…<span class="faint mono">${esc(i.value || '')}</span></button>`
        : `<input class="set-input" data-key="${esc(i.key)}" value="${esc(settingValue(i.key, i.value))}" />`;
      return `<div class="set-row"><div class="set-copy"><div class="set-key">${esc(i.key)}</div><div class="set-desc">${esc(i.desc)}</div></div>${control}</div>`;
    }).join('');
    return `<section class="set-group"><div class="set-group-title">${esc(g.title)}</div><div class="set-card">${rows}</div></section>`;
  }).join('');
  const spent = s.spendToday != null ? s.spendToday : 0.14;
  const cap = s.spendCap != null ? s.spendCap : 1.0;
  const pct = cap > 0 ? Math.min(100, Math.round((spent / cap) * 100)) : 0;
  return `<div class="settings">
    <div class="settings-hero"><div><span class="settings-eyebrow">Learning &amp; retrieval</span><h2>Make Culi work your way</h2><p>Choose how conversations become reusable knowledge, then tune the guardrails around it.</p></div><span class="config-location"><i></i><span>Local config</span><code>~/.culi/config.yaml</code></span></div>
    <section class="learning-panel">
      <div class="settings-section-head"><div><span class="settings-step">01</span><h3>Learning backend</h3><p>The model Culi uses after a session to extract durable lessons. Retrieval hooks stay fast and local.</p></div><span class="learning-privacy">Secrets never enter this page</span></div>
      <div class="backend-grid">${backends}</div>
      <div class="learning-config">
        <input class="set-input" type="hidden" data-key="provider" value="${esc(provider)}" />
        <div class="learning-config-head"><div><span>Selected backend</span><strong>${esc(selected.label || provider)}</strong></div><span class="learning-selected-auth">${esc(selected.auth || '')}</span></div>
        <div class="learning-model-grid">
          <label class="learning-field"><span>Routine model <em>high-volume mining</em></span><input class="set-input" data-key="cheap_model" value="${esc(settingValue('cheap_model', learning.cheapModel))}" /></label>
          <label class="learning-field"><span>Escalation model <em>schema retry</em></span><input class="set-input" data-key="strong_model" value="${esc(settingValue('strong_model', learning.strongModel))}" /></label>
          ${learningCredential(provider, learning)}
        </div>
      </div>
    </section>
    <div class="settings-section-head tune-head"><div><span class="settings-step">02</span><h3>Safety &amp; retrieval</h3><p>Control injection size, learning limits, card lifecycle, and watched repositories.</p></div></div>
    <div class="settings-grid">${groups}</div>
    <div class="set-spend"><div class="set-spend-copy"><span>Today’s metered learning spend</span><small>CLI subscriptions and local Ollama calls remain $0 here.</small></div><div class="set-spend-meter"><div class="set-spend-head"><span>${pct}% of daily cap</span><span class="amt">$${spent.toFixed(2)} / $${cap.toFixed(2)}</span></div><div class="bar"><div class="bar-fill" style="width:${pct}%"></div></div></div></div>
    <div class="set-btns"><div><strong>Ready to apply?</strong><span>Only safe, documented keys are written.</span></div><div class="set-btn-actions"><button class="btn btn-outline" data-act="revertConfig">Discard changes</button><button class="btn btn-teal" data-act="saveConfig">Save settings</button></div></div>
  </div>`;
}

// ---------- top-level render ----------
const SCREENS = { overview: screenOverview, review: screenReview, kb: screenKb, activity: screenActivity, reconcile: screenReconcile, settings: screenSettings };
function renderScreen() { document.getElementById('screen').innerHTML = SCREENS[state.screen](); }
function render() { renderStrip(); renderNav(); renderSpend(); renderHeader(); renderScreen(); }

// ---------- data loaders per screen ----------
async function fetchAnalyticsIntoState(showLoading) {
  const request = ++state.analyticsRequest;
  state.analyticsLoading = true;
  if (showLoading) state.analytics = null;
  if (showLoading && (state.screen === 'overview' || (state.screen === 'kb' && state.kbMode === 'analytics'))) renderScreen();
  const result = await loadAnalytics();
  if (request !== state.analyticsRequest) return;
  state.analytics = result;
  state.analyticsLoading = false;
  if (state.screen === 'overview' || (state.screen === 'kb' && state.kbMode === 'analytics')) renderScreen();
}

function beginAnalyticsLoad() {
  if (state.analytics || state.analyticsLoading) return;
  fetchAnalyticsIntoState(false);
}

async function reloadAnalytics() {
  await fetchAnalyticsIntoState(true);
}

async function ensure(screen) {
  if (!state.status) state.status = await loadStatus();
  if (screen === 'overview') {
    if (!state.overview) state.overview = await loadOverview();
    beginAnalyticsLoad();
  }
  if (screen === 'review' && !state.candidates) state.candidates = await loadCandidates();
  if (screen === 'kb') {
    if (state.kbMode === 'cards' && !state.cards) state.cards = await loadCards();
    if (state.kbMode === 'analytics' && !state.analytics && !state.analyticsLoading) await fetchAnalyticsIntoState(false);
  }
  if (screen === 'activity') {
    if (!state.sessions) {
      const r = await loadSessions();
      const obj = Array.isArray(r) ? { sessions: r, repos: [], harnesses: [] } : (r || {});
      state.sessions = obj.sessions || [];
      // Keep the last non-empty option lists so the dropdowns never blank out when
      // a filter narrows the result set (the server returns the full list anyway).
      if (obj.repos && obj.repos.length) state.actRepos = obj.repos;
      if (obj.harnesses && obj.harnesses.length) state.actHarnesses = obj.harnesses;
      state.injOpen = {};
    }
    if (!state.runs) state.runs = await loadRuns();
  }
  if (screen === 'settings' && !state.settings) state.settings = await loadSettings();
  if (screen === 'reconcile') {
    if (!state.reconcile) state.reconcile = await loadImport();
    syncMergePoll();
  }
}

// syncMergePoll starts a progress poller while a merge runs and stops it once
// the job finishes (or we leave the screen). Polling re-fetches /api/import so
// the bar advances and the Apply step lights up when staging appears.
function syncMergePoll() {
  const running = state.reconcile && state.reconcile.merge && state.reconcile.merge.running;
  if (running && !state.recPoll) {
    state.recPoll = setInterval(async () => {
      state.reconcile = await loadImport();
      if (state.screen === 'reconcile') renderScreen();
      if (!(state.reconcile.merge && state.reconcile.merge.running)) syncMergePoll();
    }, 3000);
  } else if (!running && state.recPoll) {
    clearInterval(state.recPoll);
    state.recPoll = null;
  }
}
async function goto(screen) {
  state.screen = screen;
  await ensure(screen);
  render();
}

async function openKnowledgeAnalytics() {
  state.screen = 'kb';
  state.kbMode = 'analytics';
  await ensure('kb');
  render();
}

async function setKnowledgeMode(mode) {
  state.kbMode = mode === 'analytics' ? 'analytics' : 'cards';
  await ensure('kb');
  render();
}

// ---------- review interactions ----------
function showToast(msg, dot) {
  if (toastTimer) clearTimeout(toastTimer);
  state.toast = { msg, dot };
  const t = document.getElementById('toast');
  document.getElementById('toast-dot').style.background = dot;
  document.getElementById('toast-msg').textContent = msg;
  t.hidden = false;
  toastTimer = setTimeout(() => { state.toast = null; document.getElementById('toast').hidden = true; }, 5000);
}
function resolve(verb, dot, apiVerb) {
  const cands = state.candidates;
  if (!cands || !cands.length) return;
  const idx = state.queueIndex;
  const removed = cands[idx];
  cands.splice(idx, 1);
  const newIdx = Math.min(idx, cands.length - 1);
  state.queueIndex = newIdx < 0 ? 0 : newIdx;
  state.reviewed += 1;
  state.undoStack = { card: removed, idx, verb };
  if (apiVerb) postAction(`/api/candidates/${encodeURIComponent(removed.id)}/${apiVerb}`);
  render();
  showToast(`${verb} — "${removed.title}"`, dot);
}
function approve() { resolve('Approved', COLOR.success, 'approve'); }
function reject()  { resolve('Rejected', COLOR.danger, 'reject'); }
function skip() {
  const cands = state.candidates || [];
  if (cands.length) { state.queueIndex = Math.min(cands.length - 1, state.queueIndex + 1); render(); }
  showToast('Skipped — back later', COLOR.neutral);
}
function undo() {
  if (!state.undoStack) { state.toast = null; document.getElementById('toast').hidden = true; return; }
  if (toastTimer) clearTimeout(toastTimer);
  const u = state.undoStack;
  state.candidates.splice(u.idx, 0, u.card);
  state.queueIndex = u.idx;
  state.reviewed = Math.max(0, state.reviewed - 1);
  state.toast = null; state.undoStack = null;
  document.getElementById('toast').hidden = true;
  render();
}
function moveSel(d) {
  const n = (state.candidates || []).length;
  if (!n) return;
  state.queueIndex = Math.max(0, Math.min(n - 1, state.queueIndex + d));
  render();
}
async function editCandidate() {
  const cur = (state.candidates || [])[state.queueIndex];
  if (!cur) return;
  drillTo('review');
  await gotoCard(cur.id);
  const detail = state.cardDetail[cur.id];
  if (!detail) {
    showToast('This candidate is not a persisted card yet — approve it first', COLOR.warning);
    return;
  }
  if (detail.editable === false) {
    showToast('Edit this hand-authored card in its source file', COLOR.warning);
    return;
  }
  state.kbEditing = true;
  renderScreen();
}

// ---------- event delegation ----------
const DESTRUCTIVE = new Set(['cardRetire', 'cardRemove', 'reject-noisy', 'revert', 'recDiscard']);

function splitAct(act) {
  const i = act.indexOf(':');
  return i === -1 ? [act, undefined] : [act.slice(0, i), act.slice(i + 1)];
}
function armConfirm(act) {
  state.confirm = act;
  render();
  clearTimeout(confirmTimer);
  confirmTimer = setTimeout(() => { state.confirm = null; render(); }, 3500);
}
function clearConfirm() { state.confirm = null; clearTimeout(confirmTimer); }

async function cardAction(verb, id, okLabel, okDot) {
  if (!id) return;
  const res = await postJSON('/api/cards/action', { id, action: verb });
  if (res && res.ok === false) { showToast(res.note || 'not applied', COLOR.warning); return; }
  showToast(okLabel, okDot);
  // Invalidate the caches the action can change, then re-render with fresh data.
  state.cards = null;
  state.overview = null;
  state.analytics = null;
  if (verb === 'remove') { state.kbId = null; delete state.cardDetail[id]; }
  else { const d = await loadCard(id); if (d) state.cardDetail[id] = d; else delete state.cardDetail[id]; }
  await ensure(state.screen);
  render();
}

// revertCard restores the open card to its content at a history commit. The
// server scopes the revert to this one file and records it as a fresh commit,
// so it is itself undoable from the timeline.
async function revertCard(sha) {
  const id = state.kbId;
  if (!id || !sha) return;
  const res = await postJSON('/api/cards/revert', { id, sha });
  if (res && res.ok === false) { showToast(res.note || 'could not revert', COLOR.warning); return; }
  showToast('Reverted to ' + sha, COLOR.warning);
  state.cards = null;
  state.overview = null;
  state.analytics = null;
  delete state.cardDetail[id];
  const d = await loadCard(id);
  if (d) state.cardDetail[id] = d;
  await ensure(state.screen);
  render();
}

// recRun runs a one-shot reconcile POST (scan/discard), disabling buttons while
// in flight, then refreshes the whole Import payload.
async function recRun(busyKey, path, okMsg) {
  if (state.recBusy) return;
  state.recBusy = busyKey; renderScreen();
  const res = await postJSON(path, {});
  state.recBusy = '';
  if (res && res.error) { showToast(res.error, COLOR.warning); }
  else { showToast(okMsg, COLOR.success); }
  state.reconcile = await loadImport();
  renderScreen();
}

// recStartMerge kicks off the background merge, then flips into polling so the
// progress bar animates without a manual refresh.
async function recStartMerge() {
  state.recBusy = 'recMerge'; renderScreen();
  const res = await postJSON('/api/import/merge', {});
  state.recBusy = '';
  if (res && res.ok === false) { showToast(res.note || 'could not start merge', COLOR.warning); renderScreen(); return; }
  showToast('Merge started — running in the background', COLOR.success);
  state.reconcile = await loadImport();
  renderScreen();
  syncMergePoll();
}

// recApply lands the staged cards (with the force toggle), then refreshes both
// the Import view and the caches the new cards affect.
async function recApply() {
  if (state.recBusy) return;
  state.recBusy = 'recApply'; renderScreen();
  const res = await postJSON('/api/import/apply' + (state.recForce ? '?force=1' : ''), {});
  state.recBusy = '';
  if (res && res.error) { showToast(res.error, COLOR.warning); renderScreen(); return; }
  const parts = [`${res.applied} applied`];
  if (res.conflicts) parts.push(`${res.conflicts} conflict${res.conflicts === 1 ? '' : 's'} kept`);
  if (res.residual) parts.push(`${res.residual} residual${res.residual === 1 ? '' : 's'}`);
  showToast(parts.join(' · '), COLOR.success);
  state.recForce = false;
  state.cards = null; state.overview = null; state.analytics = null; state.status = null;
  state.reconcile = await loadImport();
  render();
}

function handleAction(act) {
  const [name, arg] = splitAct(act);
  // Two-step confirm for destructive actions: first click arms ("Confirm?"),
  // second click fires. Any other click cancels a pending confirm.
  if (DESTRUCTIVE.has(name)) {
    if (state.confirm !== act) { armConfirm(act); return; }
    clearConfirm();
  } else if (state.confirm) {
    clearConfirm();
  }
  switch (name) {
    case 'nav': state.returnTo = null; goto(arg); return;
    case 'kbMode': setKnowledgeMode(arg); return;
    case 'kbAnalytics': state.returnTo = null; openKnowledgeAnalytics(); return;
    case 'analyticsHarness': {
      const next = arg === 'all' ? '' : arg;
      if (state.analyticsHarness !== next) { state.analyticsHarness = next; reloadAnalytics(); }
      return;
    }
    case 'analyticsCard':
      if (state.screen === 'overview') drillTo('overview');
      gotoCard(arg);
      return;
    // In-app back: if a browser-history drill entry exists, pop it (unifies with
    // the physical Back button via popstate); otherwise run the back directly.
    case 'back':
      if (history.state && history.state.culiBack) { history.back(); return; }
      doBack();
      return;
    case 'ovCard': drillTo('overview'); gotoCard(arg); return;
    case 'ovStale':
      drillTo('overview');
      state.staleFilter = true;
      state.typeFilter = {}; state.statusFilter = {};
      state.kbId = null;
      goto('kb');
      return;
    case 'facetStale': state.staleFilter = !state.staleFilter; state.kbId = null; renderScreen(); return;
    case 'selCand': selectCandidate(+arg); return;
    case 'approve': approve(); return;
    case 'reject': reject(); return;
    case 'skip': skip(); return;
    case 'editCandidate': editCandidate(); return;
    case 'selCard': selectCard(arg); return;
    case 'facetType': state.typeFilter[arg] = !state.typeFilter[arg]; state.kbId = null; renderScreen(); return;
    case 'facetStatus': state.statusFilter[arg] = !state.statusFilter[arg]; state.kbId = null; renderScreen(); return;
    case 'actTab': state.actTab = arg; renderScreen(); return;
    case 'actSince': if (state.actSince !== arg) { state.actSince = arg; reloadSessions(); } return;
    case 'injToggle': {
      const sc = document.getElementById('screen');
      const top = sc ? sc.scrollTop : 0;
      state.injOpen[arg] = !state.injOpen[arg];
      renderScreen();
      const sc2 = document.getElementById('screen');
      if (sc2) sc2.scrollTop = top;
      return;
    }
    case 'injCard': drillTo('activity'); gotoCard(arg); return;
    case 'undo': undo(); return;
    case 'cardDown': cardAction('down', state.kbId, 'Downvoted', COLOR.neutral); return;
    case 'cardRetire': cardAction('retire', state.kbId, 'Retired (reversible)', COLOR.warning); return;
    case 'cardRemove': cardAction('remove', state.kbId, 'Removed — recoverable from git', COLOR.danger); return;
    case 'down': cardAction('down', arg, 'Downvoted', COLOR.neutral); return;
    case 'reject-noisy': cardAction('retire', arg, 'Retired (reversible)', COLOR.warning); return;
    case 'revert': revertCard(arg); return;
    case 'setProvider': setLearningProvider(arg); return;
    case 'saveConfig': saveConfig(); return;
    case 'revertConfig': state.settingsDraft = null; state.settings = null; goto('settings'); return;
    case 'refresh': refresh(); return;
    case 'toggleTheme': toggleTheme(); return;
    case 'shortcuts': toggleShortcuts(); return;
    case 'openRepos': openRepos(); return;
    case 'repoClose': closeRepos(); return;
    case 'repoAdd': {
      const el = document.getElementById('repo-add-input');
      const p = (el ? el.value : '').trim();
      if (!p) return;
      saveRepos([...(state.repos || []).map(r => r.path), p]);
      return;
    }
    case 'repoRemove': saveRepos((state.repos || []).map(r => r.path).filter(p => p !== arg)); return;
    case 'cardEdit': {
      const cd = state.cardDetail[state.kbId];
      if (cd && cd.editable === false) { showToast('Hand-authored card — edit its file directly', COLOR.warning); return; }
      state.kbEditing = true; renderScreen(); return;
    }
    case 'kbSaveEdit': cardEditSave(); return;
    case 'kbCancelEdit': state.kbEditing = false; renderScreen(); return;
    case 'recScan': recRun('recScan', '/api/import/scan', 'Scanned repositories'); return;
    case 'recMergeArm': state.recBusy = 'recMergeArmed'; renderScreen(); return;
    case 'recMergeCancel': state.recBusy = ''; renderScreen(); return;
    case 'recMerge': recStartMerge(); return;
    case 'recApply': recApply(); return;
    case 'recDiscard': recRun('recDiscard', '/api/import/discard', 'Discarded staged merge'); return;
    case 'recForce': state.recForce = !state.recForce; renderScreen(); return;
    case 'recToggle': state.recOpen[arg] = !state.recOpen[arg]; renderScreen(); return;
    default: return;
  }
}

async function cardEditSave() {
  const g = id => { const el = document.getElementById(id); return el ? el.value : ''; };
  const res = await postJSON('/api/cards/edit', {
    id: state.kbId, title: g('kbedit-title'), scope: g('kbedit-scope'),
    key: g('kbedit-key'), keywords: g('kbedit-keywords'),
  });
  if (res && res.ok === false) { showToast(res.note || 'not saved', COLOR.warning); return; }
  showToast('Saved', COLOR.success);
  state.kbEditing = false;
  state.cards = null;
  state.analytics = null;
  delete state.cardDetail[state.kbId];
  const d = await loadCard(state.kbId);
  if (d) state.cardDetail[state.kbId] = d;
  await ensure('kb');
  render();
}

// reloadSessions re-fetches the Activity injections with the current repo/date
// filters and re-renders. A local, off-hot-path request over ≤500 rows.
async function reloadSessions() {
  state.sessions = null;
  await ensure('activity');
  renderScreen();
}

// handleChange dispatches <select>/input change events (data-change attribute),
// the counterpart to click-based data-act handling.
function handleChange(name, value) {
  if (name === 'actRepo') { state.actRepo = value; reloadSessions(); }
  else if (name === 'actHarness') { state.actHarness = value; reloadSessions(); }
  else if (name === 'analyticsRepo' && state.analyticsRepo !== value) { state.analyticsRepo = value; reloadAnalytics(); }
}

// refresh re-fetches the live data for the current screen (the dashboard is a
// snapshot; this pulls the latest without a full page reload).
async function refresh() {
  state.status = null;
  if (state.screen === 'overview') { state.overview = null; state.analytics = null; }
  else if (state.screen === 'review') state.candidates = null;
  else if (state.screen === 'kb') {
    if (state.kbMode === 'analytics') state.analytics = null;
    else { state.cards = null; state.cardDetail = {}; }
  }
  else if (state.screen === 'activity') { state.sessions = null; state.runs = null; }
  else if (state.screen === 'reconcile') state.reconcile = null;
  else if (state.screen === 'settings') state.settings = null;
  await ensure(state.screen);
  render();
  showToast('Refreshed', COLOR.success);
}

function toggleShortcuts(force) {
  const el = document.getElementById('shortcuts');
  const btn = document.querySelector('[data-act="shortcuts"]');
  const open = force != null ? force : el.hidden;
  el.hidden = !open;
  if (btn) btn.classList.toggle('active', open);
}

// ---------- repos manager modal ----------
function renderReposModal() {
  const root = document.getElementById('modal-root');
  if (!root) return;
  if (!state.reposOpen) { root.innerHTML = ''; return; }
  const repos = state.repos || [];
  const rows = repos.length ? repos.map(r => {
    const cls = r.isGit ? 'ok' : (r.exists ? 'warn' : 'bad');
    const status = r.isGit ? '' : (r.exists ? 'not a git repo' : 'path not found');
    return `<div class="repo-row">
      <span class="repo-git ${cls}" title="${status || 'git repo'}"></span>
      <span class="repo-path mono">${esc(r.path)}</span>
      ${status ? `<span class="repo-status">${status}</span>` : ''}
      <button class="repo-x" data-act="repoRemove:${esc(r.path)}" title="Remove">×</button>
    </div>`;
  }).join('') : `<div class="repo-empty">No repos watched yet — add one below.</div>`;
  root.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-backdrop" data-act="repoClose"></div>
      <div class="modal">
        <div class="modal-head"><span><span class="modal-head-title">Watched repositories</span><span class="modal-head-sub">${repos.length} · reconciled + scoped</span></span>
          <button class="repo-x" data-act="repoClose" title="Close">×</button></div>
        <div class="modal-note">For <b>learning &amp; generation</b> — mining git history and <span class="mono">culi gen</span>. Context injection works in <b>any</b> repo regardless of this list.</div>
        <div class="modal-list">${rows}</div>
        <div class="repo-add">
          <input id="repo-add-input" class="mono" placeholder="/absolute/path/to/repo" autocomplete="off" spellcheck="false" />
          <button class="btn-teal" data-act="repoAdd">Add</button>
        </div>
        <div class="modal-foot">Saved to <span class="mono">config.yaml</span> immediately — your comments are preserved.</div>
      </div>
    </div>`;
  const inp = document.getElementById('repo-add-input');
  if (inp) inp.focus();
}

async function openRepos() {
  try { state.repos = await api('/api/repos'); } catch { state.repos = []; }
  state.reposOpen = true;
  renderReposModal();
}
async function saveRepos(list) {
  const res = await postJSON('/api/repos', { repos: list });
  if (Array.isArray(res)) {
    state.repos = res;
    renderReposModal();
    showToast('Saved to config.yaml', COLOR.success);
  } else {
    showToast((res && res.error) || 'could not save repos', COLOR.warning);
  }
}
function closeRepos() {
  state.reposOpen = false;
  renderReposModal();
  state.settings = null; // refresh the Settings preview/count
  if (state.screen === 'settings') goto('settings');
}
function selectCandidate(i) {
  // Same as KB: keep the queue's scroll position when clicking a row.
  const list = document.querySelector('.queue-list');
  const top = list ? list.scrollTop : 0;
  state.queueIndex = i;
  render();
  const list2 = document.querySelector('.queue-list');
  if (list2) list2.scrollTop = top;
}
// drillTo marks which screen a KB deep-link should offer a "← back" link to,
// and pushes a browser-history entry so the physical Back button returns there
// too. Both the in-app link and the Back button funnel through doBack.
function drillTo(from) {
  state.returnTo = from;
  try { history.pushState({ culiBack: true }, ''); } catch (_) { /* file:// / blocked — link still works */ }
}
function doBack() {
  const r = state.returnTo || 'overview';
  state.returnTo = null;
  state.staleFilter = false;
  goto(r);
}
// gotoCard jumps from an injection breakdown to the card's KB detail: switch to
// the Knowledge Base, load the card, and select it. Clears any active facet
// filters so the target card is never hidden by a stale filter.
async function gotoCard(short) {
  state.screen = 'kb';
  state.kbMode = 'cards';
  state.typeFilter = {};
  state.statusFilter = {};
  state.staleFilter = false;
  state.kbEditing = false;
  await ensure('kb');
  state.kbId = short;
  if (!state.cardDetail[short]) { const d = await loadCard(short); if (d) state.cardDetail[short] = d; }
  render();
  const row = document.querySelector('.kb-row.sel');
  if (row) row.scrollIntoView({ block: 'center' });
}
async function selectCard(id) {
  state.kbId = id;
  state.kbEditing = false;
  if (!state.cardDetail[id]) { const d = await loadCard(id); if (d) state.cardDetail[id] = d; }
  // Selecting a card re-renders the whole KB screen, which recreates the list's
  // scroll container at the top. Preserve the list scroll position so the row
  // the user clicked stays put; only the detail pane changes.
  const list = document.querySelector('.kb-rows');
  const top = list ? list.scrollTop : 0;
  renderScreen();
  const list2 = document.querySelector('.kb-rows');
  if (list2) list2.scrollTop = top;
}
async function saveConfig() {
  const inputs = document.querySelectorAll('.set-input');
  const patch = {};
  inputs.forEach(i => { patch[i.dataset.key] = i.value; });
  const res = await postJSON('/api/config', patch);
  if (res && res.saved === false) {
    showToast(res.note || 'not saved', COLOR.warning);
    return;
  }
  // Server wrote config.yaml and reloaded its snapshot; refresh Settings so the
  // displayed values match what landed on disk.
  showToast('Saved to config.yaml', COLOR.success);
  state.settingsDraft = null;
  state.settings = null;
  if (state.screen === 'settings') goto('settings');
}

// ---------- keyboard ----------
function onKey(e) {
  if (e.metaKey && (e.key === 'z' || e.key === 'Z')) { if (state.toast) { e.preventDefault(); undo(); } return; }
  if (e.key === 'Escape' && state.reposOpen) { closeRepos(); return; }
  const tag = (e.target.tagName || '').toLowerCase();
  if (tag === 'input' || tag === 'textarea') {
    if (e.key === 'Escape') e.target.blur();
    if (e.key === 'Enter' && e.target.id === 'repo-add-input') {
      const p = e.target.value.trim();
      if (p) saveRepos([...(state.repos || []).map(r => r.path), p]);
    }
    return;
  }
  // Enter/Space activate a focused role="button" element (e.g. an injection row).
  if ((e.key === 'Enter' || e.key === ' ') && e.target.getAttribute &&
      e.target.getAttribute('role') === 'button' && e.target.hasAttribute('data-act')) {
    e.preventDefault(); handleAction(e.target.getAttribute('data-act')); return;
  }
  if (e.key === 'Escape') { const sh = document.getElementById('shortcuts'); if (sh && !sh.hidden) { toggleShortcuts(false); return; } }
  if (e.key === '?') { e.preventDefault(); toggleShortcuts(); return; }
  if (e.key === '/' && state.screen === 'kb') { e.preventDefault(); const s = document.getElementById('kb-search'); if (s) s.focus(); return; }
  if (state.screen !== 'review') return;
  const k = e.key.toLowerCase();
  if (k === 'a') { e.preventDefault(); approve(); }
  else if (k === 'r') { e.preventDefault(); reject(); }
  else if (k === 's') { e.preventDefault(); skip(); }
  else if (k === 'e') { e.preventDefault(); editCandidate(); }
  else if (k === 'j') { e.preventDefault(); moveSel(1); }
  else if (k === 'k') { e.preventDefault(); moveSel(-1); }
}

// ---------- boot ----------
function boot() {
  // Hash deep links are intentionally simple (`/#settings`, `/#activity`):
  // useful for bookmarks and visual smoke tests without turning this local UI
  // into a client-side router.
  const initialScreen = typeof location !== 'undefined' ? location.hash.replace(/^#/, '') : '';
  if (initialScreen && SCREENS[initialScreen]) state.screen = initialScreen;
  syncThemeControl();
  document.addEventListener('click', e => {
    const el = e.target.closest('[data-act]');
    // Close the shortcuts popover on any click outside it (and outside its toggle).
    const sh = document.getElementById('shortcuts');
    if (sh && !sh.hidden && !e.target.closest('#shortcuts') &&
        !(el && el.getAttribute('data-act') === 'shortcuts')) {
      toggleShortcuts(false);
    }
    if (!el) return;
    e.preventDefault();
    handleAction(el.getAttribute('data-act'));
  });
  document.addEventListener('input', e => {
    if (e.target && e.target.id === 'kb-search') { state.kbSearch = e.target.value; updateKbList(); }
  });
  document.addEventListener('change', e => {
    const el = e.target.closest('[data-change]');
    if (el) handleChange(el.getAttribute('data-change'), el.value);
  });
  document.getElementById('toast-undo').addEventListener('click', undo);
  window.addEventListener('keydown', onKey);
  // Physical browser Back button: if we're inside a drill-down, return to where
  // it came from — same path as the in-app "← back" link.
  window.addEventListener('popstate', () => { if (state.returnTo) doBack(); });
  try {
    const systemTheme = matchMedia('(prefers-color-scheme: dark)');
    systemTheme.addEventListener('change', e => {
      if (!localStorage.getItem(THEME_KEY)) setTheme(e.matches ? 'dark' : 'light', false);
    });
  } catch (_) { /* older browsers keep the theme chosen at page load */ }
  goto(state.screen);
}

// SEED is defined in seed.js (loaded before this file) so the console opens
// standalone. When served by `culi serve`, /api endpoints override it.
document.addEventListener('DOMContentLoaded', boot);
