/* culi Review Console — vanilla reimplementation of the design prototype.
   No framework, no build step: served by `culi serve` from embed.FS, and opens
   standalone (file://) via the SEED fallback when /api is unreachable.

   Rendering contract: discrete actions (navigate, approve/reject, toggle facet,
   select, open/close edit) re-render targeted regions. Text inputs are
   UNCONTROLLED — read from the DOM on save — so typing never triggers a
   re-render and never loses focus. */

'use strict';

// ---------- token maps (mirror README "Design Tokens") ----------
const TYPE = {
  rule:   ['R', '#3ec7bb'], style:  ['S', '#6db4ff'], lesson: ['L', '#e0a24e'],
  pattern:['P', '#c79bff'], skill:  ['K', '#57c785'], agent:  ['A', '#ff7ea8'],
};
const STATUS = {
  confirmed: ['Confirmed', 'confirmed'], candidate: ['Candidate', 'candidate'], retired: ['Retired', 'retired'],
};
const STATUS_COLOR = { confirmed: '#47d1c4', candidate: '#e6ac5c', retired: '#7b7b84' };

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
  const [letter, color] = TYPE[type] || ['?', '#8a8a93'];
  return `<span class="ticon ${cls || ''} mono" style="background:${alpha(color, 0.16)};color:${color};">${letter}</span>`;
}
function scopeChip(label, lg) {
  return `<span class="chip ${lg ? 'chip-lg' : ''} scope-${scopeTier(label)}">${esc(label)}</span>`;
}
function scopeChips(labels, lg) { return (labels || []).map(l => scopeChip(l, lg)).join(''); }

// destrBtn renders a destructive button that arms on first click. When its act
// is the one awaiting confirmation, it flips to a pulsing red "Confirm?" —
// an inline two-step guard instead of a modal (keeps the design's fast feel).
function destrBtn(act, label, cls) {
  if (state.confirm === act) return `<button class="btn btn-confirm" data-act="${esc(act)}">Confirm?</button>`;
  return `<button class="${cls}" data-act="${esc(act)}">${esc(label)}</button>`;
}

// ---------- app state ----------
const state = {
  screen: 'review',
  queueIndex: 0,
  reviewed: 0,
  editing: false,
  kbId: null,
  kbSearch: '',
  kbEditing: false,
  typeFilter: {},
  statusFilter: {},
  staleFilter: false, // KB "Stale" facet — show only never-pulled cards
  returnTo: null,     // screen to offer a "← back" link to after an Overview drill-down
  actTab: 'inj',
  actRepo: '',        // Activity: repo filter ('' = all repos)
  actSince: 'all',    // Activity: date filter (all | today | 7d | 30d)
  actRepos: [],       // repo labels seen in the recent window (filter dropdown options)
  injOpen: {}, // keyed "si:ei" — which injection events are expanded
  toast: null,
  undoStack: null,
  confirm: null, // act string of a destructive button awaiting a second click
  reposOpen: false,
  repos: null, // [{path,name,exists,isGit}] loaded from /api/repos
  // data caches (populated from /api or SEED)
  status: null, overview: null, candidates: null,
  cards: null, cardDetail: {}, sessions: null, runs: null, settings: null,
};
let toastTimer = null;
let confirmTimer = null;

// ---------- api layer (falls back to SEED on any failure) ----------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}
async function loadStatus()    { try { return await api('/api/status'); }    catch { return SEED.status; } }
async function loadOverview()  { try { return await api('/api/overview'); }  catch { return SEED.overview; } }
async function loadCandidates(){ try { return await api('/api/candidates'); }catch { return SEED.candidates; } }
async function loadCards()     { try { return await api('/api/cards'); }     catch { return SEED.cards; } }
async function loadCard(id)    { try { return await api('/api/cards/' + encodeURIComponent(id)); } catch { return (SEED.cards.find(c => c.id === id) || null); } }
async function loadSessions()  {
  const q = new URLSearchParams();
  if (state.actRepo) q.set('repo', state.actRepo);
  if (state.actSince && state.actSince !== 'all') q.set('since', state.actSince);
  const qs = q.toString();
  try { return await api('/api/activity/injections' + (qs ? '?' + qs : '')); }
  catch { return { sessions: SEED.sessions, repos: [] }; }
}
async function loadRuns()      { try { return await api('/api/activity/runs'); } catch { return SEED.runs; } }
async function loadSettings()  { try { return await api('/api/config'); }     catch { return SEED.settings; } }

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
  { key: 'overview', label: 'Overview',       icon: '◎' },
  { key: 'review',   label: 'Review',         icon: '❯' },
  { key: 'kb',       label: 'Knowledge Base', icon: '▤' },
  { key: 'activity', label: 'Activity',       icon: '◷' },
  { key: 'settings', label: 'Settings',       icon: '⚙' },
];
const TITLES = {
  overview: ['Overview', 'health snapshot'],
  review:   ['Review', () => `${(state.candidates || []).length} candidates mined from your sessions`],
  kb:       ['Knowledge Base', () => `${(state.status && state.status.cards) || (state.cards || []).length} cards`],
  activity: ['Activity', 'what culi has been doing'],
  settings: ['Settings', 'safe knobs · config.yaml'],
};

function renderStrip() {
  const st = state.status || {};
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v; };
  set('s-cards', st.cards != null ? st.cards : '—');
  set('s-saved', st.savedPct != null ? st.savedPct : '—');
  set('s-review', (state.candidates ? state.candidates.length : (st.toReview != null ? st.toReview : '—')));
  set('s-learn', st.toLearn != null ? st.toLearn : '—');
  set('s-addr', st.addr || 'localhost:7378');
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
  const nCand = (state.candidates || []).length;
  wrap.innerHTML = NAV.map(n => {
    const active = state.screen === n.key;
    const badge = n.key === 'review' && nCand > 0 ? `<span class="nav-badge">${nCand}</span>` : '';
    return `<button class="nav-item ${active ? 'active' : ''}" data-act="nav:${n.key}">
      <span class="nav-icon">${n.icon}</span><span class="nav-label">${n.label}</span>${badge}
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

// ---------- overview ----------
function screenOverview() {
  const o = state.overview || SEED.overview;
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
  const failed = (o.failedJobs && o.failedJobs.length) ? `
    <div class="failed">
      <div class="failed-head"><span class="ic">⚠</span><span class="tt">${o.failedJobs.length} failed job${o.failedJobs.length === 1 ? '' : 's'}</span></div>
      <div class="failed-line">${esc(o.failedJobs[0].kind)} — <span class="mono">${esc(o.failedJobs[0].at)}</span></div>
      <div class="failed-sub">${esc(o.failedJobs[0].reason)}</div>
      <div class="failed-btns"><button class="btn btn-red" data-act="retry">Retry</button>
        <button class="btn btn-outline" data-act="viewlog">View log</button></div>
    </div>` : '';
  const noisy = (o.noisy || []).map(c => {
    const nm = c.short
      ? `<span class="attn-name link" data-act="ovCard:${esc(c.short)}" role="button" tabindex="0" title="Open ${esc(c.name)} in the Knowledge Base">${esc(c.name)}</span>`
      : `<span class="attn-name">${esc(c.name)}</span>`;
    return `<div class="attn-row">${nm}
      <span class="score-pill">${esc(c.score)}</span>
      <button class="btn btn-outline" data-act="down:${esc(c.id || '')}">Down</button>
      ${destrBtn('reject-noisy:' + (c.id || ''), 'Reject', 'btn btn-red-outline')}</div>`;
  }).join('');
  const stale = (o.stale || []).map(c => {
    const link = c.short ? `data-act="ovCard:${esc(c.short)}" role="button" tabindex="0" title="Open ${esc(c.name)} in the Knowledge Base"` : '';
    return `<div class="attn-row ${c.short ? 'link' : ''}" ${link}>
      <span class="attn-name" style="color:var(--muted)">${esc(c.name)}</span>
      <span class="attn-last">last ${esc(c.last)}</span></div>`;
  }).join('');
  return `<div class="ov">
    <div class="ov-row">
      <div class="hero">
        <div class="hero-label">Context saved · last 7 days</div>
        <div class="hero-num-row"><div class="hero-num">${esc(o.savedPct)}</div><div class="hero-spark">${trend}</div></div>
        <div class="hero-cf"><b>${esc(o.cf.injected)}</b> tokens injected across <b>${esc(o.cf.sessions)}</b> sessions — versus <b>${esc(o.cf.vsAll)}</b> if every card loaded every session.</div>
      </div>
      <div class="tiles">${tiles}</div>
    </div>
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
        <div class="attn-head"><div class="attn-title">Noisy cards</div><div class="attn-note">injected a lot · never expanded</div></div>
        ${noisy}
      </div>
      <div class="card attn">
        <div class="attn-head"><div class="attn-title">Stale cards</div><a data-act="ovStale">${esc(o.staleHeader || '12 never pulled (30d)')} →</a></div>
        ${stale}
        <div class="attn-more link" data-act="ovStale">${esc(o.staleMore || '+ 9 more')} →</div>
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
        <span class="draft-tag">DRAFT</span></div>
      <div class="q-title">${esc(c.title)}</div>
      <div class="q-scopes">${scopeChips(c.scopeLabels)}<span class="q-ago">${esc(c.ago)}</span></div>
    </button>`;
  }).join('');
  const queueBody = cands.length ? rows : `<div class="queue-empty">Nothing queued.</div>`;
  const queue = `<div class="queue">
    <div class="queue-head"><span class="t">Candidate queue</span><span class="n">${cands.length} left</span></div>
    <div class="queue-list">${queueBody}</div>
    <div class="queue-foot"><span class="mono"><b>j/k</b> move</span><span class="mono"><b>a</b> approve</span><span class="mono"><b>r</b> reject</span><span class="mono"><b>e</b> edit</span></div>
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
    const editPanel = state.editing ? `
      <div class="edit">
        <div class="edit-head"><span class="t">Quick-fix before approving</span><a data-act="noop">Open full editor →</a></div>
        <label>Title</label><input id="edit-title" value="${esc(cur.title)}" />
        <div class="edit-2col">
          <div><label>Scope</label><input id="edit-scope" class="mono" value="${esc((cur.scopeLabels || []).join(' '))}" /></div>
          <div><label>Type</label><input id="edit-type" class="mono" value="${esc(cur.type)}" /></div>
        </div>
        <label>Trigger keywords</label><input id="edit-keywords" class="mono" value="${esc((cur.keywords || []).join(', '))}" />
        <div class="edit-btns"><button class="btn-teal" data-act="saveEdit">Save fixes</button><button class="btn-outline" data-act="cancelEdit">Cancel</button></div>
      </div>` : '';
    const body = state.editing ? '' : `<div class="cand-body md-body">${cur.body || ''}</div>`;
    const supersedes = cur.supersedes ? `
      <div class="supersedes"><div class="lbl">Approving this retires ↓</div>
        <div class="row"><span class="id">${esc(cur.supersedes.id)}</span><span class="title">${esc(cur.supersedes.title)}</span></div></div>` : '';
    focus = `<div class="focus"><div class="focus-pad">
      <div class="cand">
        <div class="cand-head"><span class="cand-draft">DRAFT CANDIDATE</span><span class="cand-guess">culi's guess — unconfirmed</span><span class="spacer"></span><span class="cand-id">${esc(cur.id)}</span></div>
        <h2 class="cand-title">${esc(cur.title)}</h2>
        <div class="cand-summary">${esc(cur.summary)}</div>
        <div class="cand-meta">
          <span class="type-pill">${typeIcon(cur.type)} ${esc(cur.type)}</span>
          ${scopeChips(cur.scopeLabels, true)}
          <span class="cand-obs">observations: <b>${esc(cur.observations)}</b></span>
        </div>
        ${editPanel}${body}${supersedes}
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
        <button class="act-edit" data-act="startEdit">Edit <span class="act-key">e</span></button>
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
function screenKb() {
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
  const staleColor = '#5fe0d3';
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
          <span class="kb-search-icon">⌕</span>
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
      <button class="hist-revert" data-act="revert:${esc(h.sha)}">Revert</button></div>`).join('');
  const retired = c.status === 'retired';
  return `<div class="kb-detail"><div class="kb-detail-pad">
    <div class="kb-d-head">
      <span class="type-pill">${typeIcon(c.type)} ${esc(c.type)}</span>
      <span class="status-badge status-${c.status}">${statusLabel}</span>${base}
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
function activityFilters() {
  const repoOpts = ['<option value="">All repos</option>'].concat(
    (state.actRepos || []).map(r => `<option value="${esc(r)}"${state.actRepo === r ? ' selected' : ''}>${esc(r)}</option>`)
  ).join('');
  const dates = [['all', 'All'], ['today', 'Today'], ['7d', '7d'], ['30d', '30d']];
  const dateSeg = dates.map(([v, l]) =>
    `<button class="seg ${state.actSince === v ? 'active' : ''}" data-act="actSince:${v}">${esc(l)}</button>`).join('');
  return `<div class="act-filters">
    <select class="act-sel mono" data-change="actRepo" title="Filter by repository">${repoOpts}</select>
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
    const filtering = !!state.actRepo || (state.actSince && state.actSince !== 'all');
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
      return `<div class="sess">
        <div class="sess-head">
          ${repoChip(s)}
          <span class="sess-id mono" title="Claude Code conversation (session id)">${esc(s.id)}</span>
          <span class="sess-time">${esc(s.time)}</span>
          <span class="spacer"></span>
          <span class="sess-nev">${nEvents} event${nEvents === 1 ? '' : 's'}</span>
          <span class="sess-tok mono">${esc(s.tokens)} tok</span>
        </div>
        <div class="sess-box">${events}<div class="sess-foot">↳ click any row to see exactly which cards Claude was shown</div></div>
      </div>`;
    }).join('');
    body = sessions || (filtering
      ? `<div class="act-empty"><div class="act-empty-ic">◷</div>
        <div class="act-empty-t">No injections match this filter</div>
        <div class="act-empty-s">Widen the repo or date range to see more recent activity.</div></div>`
      : `<div class="act-empty"><div class="act-empty-ic">◷</div>
        <div class="act-empty-t">No injections logged yet</div>
        <div class="act-empty-s">Work in a watched repo and culi's injections will show up here, grouped by conversation.</div></div>`);
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

// ---------- settings ----------
function screenSettings() {
  const s = state.settings || SEED.settings;
  const groups = s.groups.map(g => {
    const rows = g.items.map(i => {
      const control = i.key === 'repos'
        ? `<button class="btn btn-outline set-repos-btn" data-act="openRepos">Manage…<span class="faint mono">${esc(i.value || '')}</span></button>`
        : `<input class="set-input" style="width:${i.width}" data-key="${esc(i.key)}" value="${esc(i.value)}" />`;
      return `<div class="set-row"><div style="flex:1"><div class="set-key">${esc(i.key)}</div><div class="set-desc">${esc(i.desc)}</div></div>${control}</div>`;
    }).join('');
    return `<div class="set-group"><div class="set-group-title">${esc(g.title)}</div><div class="set-card">${rows}</div></div>`;
  }).join('');
  const spent = s.spendToday != null ? s.spendToday : 0.14;
  const cap = s.spendCap != null ? s.spendCap : 1.0;
  const pct = cap > 0 ? Math.min(100, Math.round((spent / cap) * 100)) : 0;
  return `<div class="settings">
    <div class="settings-note">Writes go to <span class="mono">config.yaml</span>. Values below are the safe knobs.</div>
    ${groups}
    <div class="set-spend"><div class="set-spend-head"><span>Spend today vs daily cap</span><span class="amt">$${spent.toFixed(2)} / $${cap.toFixed(2)}</span></div>
      <div class="bar"><div class="bar-fill" style="width:${pct}%"></div></div></div>
    <div class="set-btns"><button class="btn btn-teal" data-act="saveConfig">Save to config.yaml</button><button class="btn btn-outline" data-act="revertConfig">Revert</button></div>
  </div>`;
}

// ---------- top-level render ----------
const SCREENS = { overview: screenOverview, review: screenReview, kb: screenKb, activity: screenActivity, settings: screenSettings };
function renderScreen() { document.getElementById('screen').innerHTML = SCREENS[state.screen](); }
function render() { renderStrip(); renderNav(); renderSpend(); renderHeader(); renderScreen(); }

// ---------- data loaders per screen ----------
async function ensure(screen) {
  if (!state.status) state.status = await loadStatus();
  if (screen === 'overview' && !state.overview) state.overview = await loadOverview();
  if (screen === 'review' && !state.candidates) state.candidates = await loadCandidates();
  if (screen === 'kb' && !state.cards) state.cards = await loadCards();
  if (screen === 'activity') {
    if (!state.sessions) {
      const r = await loadSessions();
      const obj = Array.isArray(r) ? { sessions: r, repos: [] } : (r || {});
      state.sessions = obj.sessions || [];
      // Keep the last non-empty repo list so the dropdown never blanks out when a
      // filter narrows the result set (the server returns the full list anyway).
      if (obj.repos && obj.repos.length) state.actRepos = obj.repos;
      state.injOpen = {};
    }
    if (!state.runs) state.runs = await loadRuns();
  }
  if (screen === 'settings' && !state.settings) state.settings = await loadSettings();
}
async function goto(screen) {
  state.screen = screen;
  await ensure(screen);
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
  state.editing = false;
  state.undoStack = { card: removed, idx, verb };
  if (apiVerb) postAction(`/api/candidates/${encodeURIComponent(removed.id)}/${apiVerb}`);
  render();
  showToast(`${verb} — "${removed.title}"`, dot);
}
function approve() { resolve('Approved', '#3ec7bb', 'approve'); }
function reject()  { resolve('Rejected', '#ff5b52', 'reject'); }
function skip() {
  const cands = state.candidates || [];
  if (cands.length) { state.queueIndex = Math.min(cands.length - 1, state.queueIndex + 1); state.editing = false; render(); }
  showToast('Skipped — back later', '#8a8a93');
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
  state.editing = false;
  render();
}
function startEdit() { if ((state.candidates || [])[state.queueIndex]) { state.editing = true; render(); } }
function cancelEdit() { state.editing = false; render(); }
function saveEdit() {
  const cur = (state.candidates || [])[state.queueIndex];
  if (!cur) return;
  const g = id => { const el = document.getElementById(id); return el ? el.value : ''; };
  cur.title = g('edit-title');
  const scope = g('edit-scope').trim();
  if (scope) cur.scopeLabels = scope.split(/\s+/);
  cur.type = g('edit-type').trim() || cur.type;
  cur.keywords = g('edit-keywords').split(',').map(s => s.trim()).filter(Boolean);
  state.editing = false;
  postAction(`/api/candidates/${encodeURIComponent(cur.id)}/edit`);
  render();
  showToast('Saved fixes', '#3ec7bb');
}

// ---------- event delegation ----------
const DESTRUCTIVE = new Set(['cardRetire', 'cardRemove', 'reject-noisy']);

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
  if (res && res.ok === false) { showToast(res.note || 'not applied', '#e6ac5c'); return; }
  showToast(okLabel, okDot);
  // Invalidate the caches the action can change, then re-render with fresh data.
  state.cards = null;
  state.overview = null;
  if (verb === 'remove') { state.kbId = null; delete state.cardDetail[id]; }
  else { const d = await loadCard(id); if (d) state.cardDetail[id] = d; else delete state.cardDetail[id]; }
  await ensure(state.screen);
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
    case 'startEdit': startEdit(); return;
    case 'saveEdit': saveEdit(); return;
    case 'cancelEdit': cancelEdit(); return;
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
    case 'cardDown': cardAction('down', state.kbId, 'Downvoted', '#8a8a93'); return;
    case 'cardRetire': cardAction('retire', state.kbId, 'Retired (reversible)', '#e6ac5c'); return;
    case 'cardRemove': cardAction('remove', state.kbId, 'Removed — recoverable from git', '#ff5b52'); return;
    case 'down': cardAction('down', arg, 'Downvoted', '#8a8a93'); return;
    case 'reject-noisy': cardAction('retire', arg, 'Retired (reversible)', '#e6ac5c'); return;
    case 'retry': showToast("Retry isn't wired yet — re-run `culi learn` in a terminal", '#e6ac5c'); return;
    case 'revert': showToast('Not wired — run `git revert ' + (arg || '') + '` in ~/.culi/knowledge', '#8a8a93'); return;
    case 'saveConfig': saveConfig(); return;
    case 'revertConfig': state.settings = null; goto('settings'); return;
    case 'refresh': refresh(); return;
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
      if (cd && cd.editable === false) { showToast('Hand-authored card — edit its file directly', '#e6ac5c'); return; }
      state.kbEditing = true; renderScreen(); return;
    }
    case 'kbSaveEdit': cardEditSave(); return;
    case 'kbCancelEdit': state.kbEditing = false; renderScreen(); return;
    case 'noop': case 'viewlog': return;
    default: return;
  }
}

async function cardEditSave() {
  const g = id => { const el = document.getElementById(id); return el ? el.value : ''; };
  const res = await postJSON('/api/cards/edit', {
    id: state.kbId, title: g('kbedit-title'), scope: g('kbedit-scope'),
    key: g('kbedit-key'), keywords: g('kbedit-keywords'),
  });
  if (res && res.ok === false) { showToast(res.note || 'not saved', '#e6ac5c'); return; }
  showToast('Saved', '#3ec7bb');
  state.kbEditing = false;
  state.cards = null;
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
}

// refresh re-fetches the live data for the current screen (the dashboard is a
// snapshot; this pulls the latest without a full page reload).
async function refresh() {
  state.status = null;
  if (state.screen === 'overview') state.overview = null;
  else if (state.screen === 'review') state.candidates = null;
  else if (state.screen === 'kb') { state.cards = null; state.cardDetail = {}; }
  else if (state.screen === 'activity') { state.sessions = null; state.runs = null; }
  else if (state.screen === 'settings') state.settings = null;
  await ensure(state.screen);
  render();
  showToast('Refreshed', '#3ec7bb');
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
    showToast('Saved to config.yaml', '#3ec7bb');
  } else {
    showToast((res && res.error) || 'could not save repos', '#e6ac5c');
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
  state.editing = false;
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
    showToast(res.note || 'not saved', '#e6ac5c');
    return;
  }
  // Server wrote config.yaml and reloaded its snapshot; refresh Settings so the
  // displayed values match what landed on disk.
  showToast('Saved to config.yaml', '#3ec7bb');
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
  if (state.editing) { if (e.key === 'Escape') cancelEdit(); return; }
  const k = e.key.toLowerCase();
  if (k === 'a') { e.preventDefault(); approve(); }
  else if (k === 'r') { e.preventDefault(); reject(); }
  else if (k === 's') { e.preventDefault(); skip(); }
  else if (k === 'e') { e.preventDefault(); startEdit(); }
  else if (k === 'j') { e.preventDefault(); moveSel(1); }
  else if (k === 'k') { e.preventDefault(); moveSel(-1); }
}

// ---------- boot ----------
function boot() {
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
  goto(state.screen);
}

// SEED is defined in seed.js (loaded before this file) so the console opens
// standalone. When served by `culi serve`, /api endpoints override it.
document.addEventListener('DOMContentLoaded', boot);
