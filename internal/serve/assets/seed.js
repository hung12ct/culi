/* Standalone fallback data — mirrors the design prototype so index.html opens
   over file:// with no server. When `culi serve` is running, every /api/*
   endpoint overrides these values; nothing here reaches a real deployment. */

const SEED = {
  status: {
    cards: 140, savedPct: '~99%', toReview: 4, addr: 'localhost:7378',
    spendToday: 0.14, spendCap: 1.0, failedJobs: 1, ollamaOffline: true, serveDown: false,
  },
  overview: {
    savedPct: '~99%',
    trend: [30, 40, 36, 52, 46, 60, 55, 68, 64, 78, 74, 88],
    cf: { injected: '24,397', sessions: '59', vsAll: '3.6M' },
    tiles: [
      { label: 'Cards', value: '140', sub: '84R · 10S · 25P · 14K · 7A', color: '#f2f2f5' },
      { label: 'To review', value: '4', sub: 'candidates queued', color: '#e6ac5c' },
      { label: 'Gate-skip rate', value: '62%', sub: 'free prompts', color: '#5fe0d3' },
      { label: 'Spend today', value: '$0.14', sub: '38 calls', color: '#f2f2f5' },
    ],
    granularity: { body: 12, summary: 46, hook: 42 },
    failedJobs: [{ kind: 'learn run', at: '2026-07-20 08:12', reason: 'ollama embed timeout · fell back to keyword' }],
    noisy: [
      { id: 'skills/worktree', name: 'skills/worktree', score: '4.3' },
      { id: 'rule/go-errwrap', name: 'rule/go-errwrap', score: '3.8' },
      { id: 'style/commit-msg', name: 'style/commit-msg', score: '3.1' },
    ],
    staleHeader: '12 never pulled (30d)', staleMore: '+ 9 more',
    stale: [
      { name: 'import/legacy-makefile', last: '41d' },
      { name: 'pattern/old-ci', last: '38d' },
      { name: 'rule/py2-compat', last: '52d' },
    ],
  },
  candidates: [
    { id: 'e7c2', type: 'pattern', title: 'Prefer table-driven tests in Go',
      summary: 'Group related cases into a slice of structs and loop with t.Run — culi saw this pattern three times this week.',
      scopeLabels: ['lang:go'], observations: 3, keywords: ['test', 'table', 'subtests', 't.Run'],
      source: 'learn', model: 'claude-haiku', created: '2026-07-20 09:14', ago: '2h ago', supersedes: null,
      evidence: '"…let me refactor these into a table-driven test so we can add cases without duplicating the setup — cases := []struct{…}"',
      body: '<p>When a function has several input/output cases, express them as a slice of structs and iterate with <code>t.Run(tc.name, …)</code> so each case reports independently.</p><ul><li>Name every case — failures point straight to the row.</li><li>Keep setup outside the loop; only the assertion varies.</li><li>Use <code>t.Parallel()</code> inside the closure when cases are independent.</li></ul>' },
    { id: 'b19a', type: 'rule', title: 'Run `phin fmt` before every commit',
      summary: 'Formatter must pass before commit; culi noticed manual gofmt invocations that phin fmt already wraps.',
      scopeLabels: ['repo:phin'], observations: 1, keywords: ['commit', 'format', 'phin fmt'],
      source: 'learn', model: 'claude-haiku', created: '2026-07-20 08:41', ago: '3h ago',
      evidence: '"CI is red again on formatting — just run phin fmt, it wraps gofmt + goimports + the license header."',
      supersedes: { id: '4d0e', title: 'Run gofmt before commit' },
      body: '<p>The repo ships <code>phin fmt</code>, which runs <code>gofmt</code>, <code>goimports</code>, and the license-header check together. Always run it (or the pre-commit hook) before committing — CI rejects unformatted diffs.</p>' },
    { id: 'f3d8', type: 'lesson', title: 'Vietnamese aliases pin gate keywords',
      summary: 'Gate matched English keywords only; the operator prompts bilingually, so aliases should include VN terms.',
      scopeLabels: ['global'], observations: 2, keywords: ['gate', 'alias', 'i18n'],
      source: 'learn', model: 'claude-haiku', created: '2026-07-20 07:55', ago: '4h ago', supersedes: null,
      evidence: '"gate bỏ qua cái này — thêm alias tiếng Việt cho \'triển khai\' = deploy"',
      body: '<p>The keyword gate only matched English terms, so bilingual prompts skipped relevant cards. Add Vietnamese aliases (e.g. <code>triển khai → deploy</code>, <code>kiểm thử → test</code>) to high-value cards so the gate fires regardless of prompt language.</p>' },
    { id: 'a920', type: 'pattern', title: 'Debounce cursor events at 16ms in Cutpilot',
      summary: 'Cursor-move handler fired per raw event; batching to one frame removed jank in the overlay.',
      scopeLabels: ['repo:cutpilot'], observations: 2, keywords: ['debounce', 'cursor', 'raf', 'overlay'],
      source: 'learn', model: 'claude-haiku', created: '2026-07-20 06:30', ago: '6h ago', supersedes: null,
      evidence: '"the overlay stutters — coalesce cursormove to one rAF, ~16ms, instead of handling every event"',
      body: '<p>Coalesce high-frequency <code>cursormove</code> events to a single <code>requestAnimationFrame</code> (~16ms) before touching the overlay. Handling every raw event caused layout thrash and visible stutter.</p>' },
  ],
  cards: (function () {
    const u = (a, b, c, d) => [
      { label: 'injected', value: a, color: '#47d1c4' }, { label: 'expanded', value: b, color: '#e4e4e8' },
      { label: 'referenced', value: c, color: '#e4e4e8' }, { label: 'downvoted', value: d, color: '#ff8079' }];
    const sp = () => [55, 70, 40, 85, 60, 75, 50];
    return [
      { id: 'a3f1', type: 'rule', title: 'Wrap errors with %w, never %v', status: 'confirmed', scopeLabels: ['lang:go'], baseline: true, injected: 214, key: 'go/errors', source: 'import', mergedFrom: 'phin, cutpilot', model: 'claude-sonnet', hash: '9f3c…a1', spark: sp(), triggers: ['error', 'wrap', 'errors.Is', 'fmt.Errorf', 'lỗi'], usage: u('214', '61', '18', '0'),
        body: '<p>Always wrap propagated errors with <code>fmt.Errorf("…: %w", err)</code> so callers can <code>errors.Is</code>/<code>errors.As</code> the chain. Reserve <code>%v</code> for terminal logging only.</p><h4>Why</h4><p>Losing the wrapped error breaks sentinel checks downstream and makes root-cause analysis guesswork.</p>',
        history: [{ action: 'learn:', detail: 'reinforced from phin session', date: '2026-07-19', sha: 'a1b2c3d', dot: '#3ec7bb' }, { action: 'merge:', detail: 'merged duplicate from cutpilot', date: '2026-06-30', sha: '7788aa1', dot: '#6db4ff' }, { action: 'import:', detail: 'created from CLAUDE.md', date: '2026-05-02', sha: '0011ff9', dot: '#8a8a93' }] },
      { id: 'c8b2', type: 'style', title: 'Commit subjects: imperative, ≤50 chars', status: 'confirmed', scopeLabels: ['global'], baseline: true, injected: 188, key: 'git/commit', source: 'manual', mergedFrom: '—', model: '—', hash: '2c4d…9b', spark: sp(), triggers: ['commit', 'message', 'git'], usage: u('188', '40', '12', '2'),
        body: '<p>Write commit subjects in the imperative mood ("Add", not "Added"), keep them under 50 characters, and put detail in the body separated by a blank line.</p>',
        history: [{ action: 'manual:', detail: 'edited scope to global', date: '2026-07-01', sha: 'ee44bb2', dot: '#e6ac5c' }, { action: 'import:', detail: 'created manually', date: '2026-04-11', sha: '33aa11c', dot: '#8a8a93' }] },
      { id: 'd4e9', type: 'pattern', title: 'Debounce cursor events at 16ms', status: 'candidate', scopeLabels: ['repo:cutpilot'], baseline: false, injected: 3, key: '', source: 'learn', mergedFrom: '—', model: 'claude-haiku', hash: 'aa90…c2', spark: sp(), triggers: ['debounce', 'cursor', 'raf'], usage: u('3', '1', '0', '0'),
        body: '<p>Coalesce <code>cursormove</code> to a single rAF (~16ms) before touching the overlay.</p>',
        history: [{ action: 'learn:', detail: 'candidate created', date: '2026-07-20', sha: 'ff0099a', dot: '#e6ac5c' }] },
      { id: 'b710', type: 'skill', title: 'Set up a git worktree for parallel work', status: 'confirmed', scopeLabels: ['global'], baseline: false, injected: 96, key: 'git/worktree', source: 'gen', mergedFrom: '—', model: 'claude-sonnet', hash: '55ab…7d', spark: sp(), triggers: ['worktree', 'branch', 'parallel'], usage: u('96', '9', '3', '4'),
        body: '<p>Use <code>git worktree add ../feature feature</code> to check out a second branch without stashing. Remove with <code>git worktree remove</code>.</p>',
        history: [{ action: 'gen:', detail: 'generated from docs', date: '2026-06-18', sha: '9a8b7c6', dot: '#57c785' }] },
      { id: '6f2c', type: 'agent', title: 'Reviewer agent: security-focused pass', status: 'confirmed', scopeLabels: ['repo:phin'], baseline: false, injected: 41, key: 'agent/review', source: 'mcp', mergedFrom: '—', model: 'claude-sonnet', hash: 'cd12…4e', spark: sp(), triggers: ['review', 'security', 'audit'], usage: u('41', '20', '11', '1'),
        body: '<p>Sub-agent prompt that reviews a diff for injection, authz, and secret-handling issues before merge.</p>',
        history: [{ action: 'mcp:', detail: 'synced from registry', date: '2026-07-10', sha: '4e5f6a7', dot: '#ff7ea8' }] },
      { id: '9d01', type: 'lesson', title: 'py2-compat shims no longer needed', status: 'retired', scopeLabels: ['lang:python'], baseline: false, injected: 0, key: '', source: 'import', mergedFrom: '—', model: '—', hash: '77ce…10', spark: [10, 8, 6, 4, 2, 0, 0], triggers: ['python2', 'compat', '__future__'], usage: u('0', '0', '0', '6'),
        body: '<p><em>Retired.</em> The codebase dropped Python 2 support; <code>__future__</code> imports and six shims are obsolete.</p>',
        history: [{ action: 'retire:', detail: 'never pulled in 52d', date: '2026-07-15', sha: 'dd00aa1', dot: '#e6ac5c' }, { action: 'import:', detail: 'created from legacy docs', date: '2025-11-20', sha: '1122ff0', dot: '#8a8a93' }] },
      { id: '3a55', type: 'rule', title: 'Never log secrets, even at debug level', status: 'confirmed', scopeLabels: ['global'], baseline: true, injected: 176, key: 'sec/logging', source: 'manual', mergedFrom: '—', model: '—', hash: '88fa…22', spark: sp(), triggers: ['log', 'secret', 'token', 'redact'], usage: u('176', '33', '9', '0'),
        body: '<p>Redact tokens, keys, and PII before logging at any level. Prefer structured logging with an explicit allowlist of fields.</p>',
        history: [{ action: 'manual:', detail: 'created manually', date: '2026-03-02', sha: '99cc22a', dot: '#8a8a93' }] },
      { id: '1e88', type: 'style', title: 'Prefer early returns over nested ifs', status: 'confirmed', scopeLabels: ['lang:go', 'lang:ts'], baseline: false, injected: 122, key: '', source: 'gen', mergedFrom: '—', model: 'claude-sonnet', hash: '0a0a…33', spark: sp(), triggers: ['guard', 'early return', 'nesting'], usage: u('122', '14', '5', '1'),
        body: '<p>Handle error/edge cases first and return early to keep the happy path flat and unindented.</p>',
        history: [{ action: 'gen:', detail: 'generated', date: '2026-05-28', sha: '5b5b5b5', dot: '#57c785' }] },
    ];
  })(),
  sessions: [
    { id: 'sess-3f9a', repo: 'phin', time: 'today 10:42', tokens: '1,204', events: [
      { at: '10:42', ev: 'session-start', cards: '5 baseline cards', gran: 'summary', tok: '612 tok', list: [
        { id: 'rules/go-error-wrapping', short: 'e7a1', gran: 'summary', tok: 58 },
        { id: 'rules/ctx-first', short: 'b2c4', gran: 'summary', tok: 44 },
        { id: 'skills/git-commit', short: '9f31', gran: 'summary', tok: 61 },
        { id: 'styles/go-naming', short: 'aa02', gran: 'hook', tok: 16 },
        { id: 'rules/no-panic', short: 'c810', gran: 'hook', tok: 15 }] },
      { at: '10:47', ev: 'user-prompt', cards: 'rules/go-error-wrapping, skills/git-commit', gran: 'hook', tok: '188 tok', list: [
        { id: 'rules/go-error-wrapping', short: 'e7a1', gran: 'hook', tok: 16 },
        { id: 'skills/git-commit', short: '9f31', gran: 'hook', tok: 17 }] },
      { at: '10:53', ev: 'user-prompt', cards: 'rules/phin-fmt +2 more', gran: 'body+hook', tok: '404 tok', list: [
        { id: 'rules/phin-fmt', short: 'd4e9', gran: 'body', tok: 372 },
        { id: 'styles/go-naming', short: 'aa02', gran: 'hook', tok: 16 },
        { id: 'rules/removed-card', short: '', gran: 'hook', tok: 16 }] }] },
    { id: 'sess-2b71', repo: 'cutpilot', time: 'today 09:15', tokens: '806', events: [
      { at: '09:15', ev: 'session-start', cards: '5 baseline cards', gran: 'summary', tok: '598 tok', list: [] },
      { at: '09:22', ev: 'user-prompt', cards: 'patterns/debounce-cursor', gran: 'hook', tok: '208 tok', list: [
        { id: 'patterns/debounce-cursor', short: 'a920', gran: 'hook', tok: 18 }] }] },
    { id: 'sess-1c40', repo: 'phin', time: 'yesterday 16:30', tokens: '1,533', events: [
      { at: '16:30', ev: 'session-start', cards: '5 baseline cards', gran: 'summary', tok: '605 tok', list: [] },
      { at: '16:41', ev: 'user-prompt', cards: 'skills/git-worktree, agents/reviewer', gran: 'body', tok: '928 tok', list: [
        { id: 'skills/git-worktree', short: '3b1f', gran: 'body', tok: 540 },
        { id: 'agents/reviewer', short: '77ac', gran: 'body', tok: 388 }] }] },
  ],
  runs: [
    { date: '2026-07-20 08:12', mined: '31', clean: '27', candidates: '4', patterns: '6', spend: '$0.09', dot: '#ff5b52', failed: true },
    { date: '2026-07-13 08:10', mined: '44', clean: '39', candidates: '7', patterns: '9', spend: '$0.12', dot: '#57c785', failed: false },
    { date: '2026-07-06 08:11', mined: '28', clean: '25', candidates: '3', patterns: '5', spend: '$0.07', dot: '#57c785', failed: false },
  ],
  settings: {
    spendToday: 0.14, spendCap: 1.0,
    groups: [
      { title: 'Budgets', items: [
        { key: 'push_budget_tokens', desc: 'max tokens injected per prompt', value: '1500', width: '90px' },
        { key: 'baseline_budget_tokens', desc: 'session-start baseline cap', value: '700', width: '90px' }] },
      { title: 'Lifecycle', items: [
        { key: 'candidate_ttl_days', desc: 'auto-expire unreviewed candidates', value: '14', width: '70px' },
        { key: 'stale_after_days', desc: 'flag cards never pulled', value: '30', width: '70px' }] },
      { title: 'Caps & spend', items: [
        { key: 'daily_cap_usd', desc: 'hard stop on learn spend', value: '1.00', width: '80px' },
        { key: 'max_calls_per_run', desc: 'model calls per learn run', value: '60', width: '70px' }] },
      { title: 'Gate', items: [
        { key: 'extra_acks', desc: 'always-fire keywords', value: 'deploy, ship', width: '160px' },
        { key: 'stopwords', desc: 'never trigger on these', value: 'the, a, is', width: '160px' },
        { key: 'repos', desc: 'watched repositories', value: 'phin, cutpilot', width: '160px' }] },
    ],
  },
};
