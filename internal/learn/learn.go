// Package learn is the background learning worker: it drains the SessionEnd
// inbox through the mining pipeline under a lockfile, per-transcript cursors,
// and the llmtier spend caps. Spawned detached by the session-end hook and
// runnable manually as `culi learn`. It must be safe to run at any moment:
// locked ⇒ exit quietly; capped ⇒ stop, keep the queue; no backend ⇒ report
// options, keep the queue.
package learn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/learn/mine"
	"github.com/hung12ct/culi/internal/learn/patterns"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/learn/style"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

// embedBudget bounds the post-run vector pass for freshly written cards —
// best-effort, like every Ollama touch.
const embedBudget = 5 * time.Second

// parallelDrainWorkers bounds concurrent MineSession calls in a --no-cap drain.
// Small on purpose: the LLM calls overlap, but the store writes serialize on a
// single WAL writer, so more workers buy little and just add subprocess load.
const parallelDrainWorkers = 4

// Summary reports one worker run.
type Summary struct {
	Backend     string
	Jobs        int // jobs seen
	Mined       int // sessions that produced windows and a model call
	Clean       int // sessions with zero windows (free)
	Created     []string
	Reinforced  []string
	Confirmed   []string
	Retired     []string
	Attributed  int // cards credited for observed use (usage attribution)
	StyleObs    int
	Style       style.Result    // pipeline B synthesis, when it fired
	Patterns    patterns.Result // pipeline D branch index
	Usage       llmgen.Usage
	Notes       []string
	Capped      bool
	BackendDown bool // backend unavailable (CLI logged out / bad key) — run halted, jobs kept queued
}

// Options tunes one worker run.
type Options struct {
	FromStart  bool // ignore cursors, re-mine transcripts from the beginning
	ForceStyle bool // bypass the style-synthesis trigger policy
	IgnoreCaps bool // drain the whole backlog now, ignoring the daily USD/call caps
}

// Run drains the inbox once and fires the policy-gated style synthesis.
// Errors that concern one job go to Notes and the fail ladder; only setup
// failures return an error.
func Run(ctx context.Context, base string, cfg config.Config, opts Options, logf func(string, ...any)) (Summary, error) {
	var sum Summary
	if !cfg.Learn.Enabled {
		sum.Notes = append(sum.Notes, "learning disabled (learn.enabled: false) — jobs stay queued")
		return sum, nil
	}
	stateDir := config.StateDir(base)
	lock, err := queue.TryLock(stateDir)
	if errors.Is(err, queue.ErrLocked) {
		sum.Notes = append(sum.Notes, "another learn worker is running")
		return sum, nil
	}
	if err != nil {
		return sum, err
	}
	defer lock.Unlock()

	jobs, err := queue.List(config.InboxDir(base))
	if err != nil {
		return sum, err
	}
	sum.Jobs = len(jobs)
	jobs = applyRunLimits(&cfg, opts, jobs, &sum)

	tier, desc, err := llmtier.Resolve(cfg.Learn, cfg.Ollama.Endpoint, stateDir)
	if err != nil {
		return sum, err
	}
	sum.Backend = desc
	if tier == nil {
		// No backend: leave every job queued — the pointers are tiny and the
		// queue drains the moment the user configures one.
		sum.Notes = append(sum.Notes, desc)
		return sum, nil
	}

	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return sum, err
	}
	defer s.Close()

	w := &worker{
		base: base, cfg: cfg, opts: opts, logf: logf, store: s, tier: tier,
		emb:     embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model, cfg.Ollama.KeepAlive),
		cursors: queue.LoadCursors(stateDir), now: time.Now().UTC(), sum: sum,
	}
	if opts.IgnoreCaps && len(jobs) > 1 {
		w.mineParallel(ctx, jobs)
	} else {
		w.mineSerial(ctx, jobs)
	}
	w.afterMining(ctx)
	return w.sum, nil
}

// applyRunLimits applies --no-cap or learn.max_jobs_per_run to this run.
// cfg is the run's local copy, so zeroing its caps affects this run only.
func applyRunLimits(cfg *config.Config, opts Options, jobs []queue.Job, sum *Summary) []queue.Job {
	// --no-cap: drain the whole backlog in one run. Zero caps disable the gate
	// (see ledger.Allow), so this deliberately bypasses daily_usd_cap /
	// daily_call_cap for this run only.
	if opts.IgnoreCaps {
		cfg.Learn.DailyUSDCap = 0
		cfg.Learn.DailyCallCap = 0
		sum.Notes = append(sum.Notes, "caps ignored (--no-cap): draining the full backlog")
		return jobs
	}
	if lim := cfg.Learn.MaxJobsPerRun; lim > 0 && len(jobs) > lim {
		// Mine only the newest N this run (jobs are newest-first); the rest wait
		// for the next run or a --no-cap drain.
		sum.Notes = append(sum.Notes, fmt.Sprintf("this run: %d newest of %d queued (learn.max_jobs_per_run; --no-cap for all)", lim, len(jobs)))
		return jobs[:lim]
	}
	return jobs
}

// worker is one learn run's shared state. Its methods run on the run's main
// goroutine unless noted, so sum and cursors need no locks.
type worker struct {
	base    string
	cfg     config.Config
	opts    Options
	logf    func(string, ...any)
	store   *store.Store
	tier    *llmtier.Tier
	emb     embed.Embedder
	cursors *queue.Cursors
	now     time.Time
	sum     Summary
}

func (w *worker) newMiner(writeMu *sync.Mutex) *mine.Miner {
	return &mine.Miner{
		Base: w.base, Store: w.store, Tier: w.tier, Emb: w.emb, EmbModel: w.cfg.Ollama.Model,
		ConfirmAt: w.cfg.Learn.ConfirmAt, Logf: w.logf, WriteMu: writeMu,
	}
}

// prep filters a job to a mineable cursor; ok=false means nothing new (the
// job is already marked done).
func (w *worker) prep(job queue.Job) (queue.Cursor, bool) {
	st, err := os.Stat(job.TranscriptPath)
	if err != nil {
		w.sum.Notes = append(w.sum.Notes, fmt.Sprintf("transcript gone: %s", job.TranscriptPath))
		_ = queue.Done(job)
		return queue.Cursor{}, false
	}
	cur := w.cursors.Get(job.TranscriptPath)
	if w.opts.FromStart {
		cur = queue.Cursor{}
	}
	if !queue.ShouldMine(cur, st.Size(), job.IsFinal(), w.now) {
		_ = queue.Done(job) // nothing new since the last mine
		return queue.Cursor{}, false
	}
	return cur, true
}

// collect folds one finished mine into the summary and advances the cursor
// and queue. stop=true halts the run (caps/backend), leaving the remaining
// jobs queued.
func (w *worker) collect(job queue.Job, res mine.Result, newCur queue.Cursor, err error) (stop bool) {
	w.sum.Usage.Add(res.Usage)
	switch {
	case errors.Is(err, llmtier.ErrCapped):
		w.sum.Capped = true
		w.sum.Notes = append(w.sum.Notes, firstNoteLine(err))
		return true
	case errors.Is(err, llmtier.ErrBackendUnavailable):
		w.sum.BackendDown = true
		w.sum.Notes = append(w.sum.Notes, firstNoteLine(err))
		return true
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return false // run halted/timed out — leave the job queued, don't park
	case err != nil:
		w.failJob(job, err)
		return false
	}
	// Parse-gated deletion: only after the result landed do cursor + job
	// advance, in that order — a crash between the two re-mines (dedup
	// absorbs it) rather than losing a region.
	w.cursors.Set(job.TranscriptPath, newCur)
	if err := w.cursors.Save(); err != nil {
		w.sum.Notes = append(w.sum.Notes, err.Error())
	}
	_ = queue.Done(job)
	w.sum.addResult(res)
	return false
}

func (w *worker) failJob(job queue.Job, cause error) {
	parked, ferr := queue.Fail(job, cause)
	note := fmt.Sprintf("job %s failed (attempt %d): %v", job.SessionID, job.Attempts()+1, cause)
	if parked {
		note += " (moved to inbox/failed; see culi learn --failed)"
	}
	if ferr != nil {
		note += " (" + ferr.Error() + ")"
	}
	w.sum.Notes = append(w.sum.Notes, note)
}

// addResult folds one successful mine into the summary.
func (sum *Summary) addResult(res mine.Result) {
	if res.Windows == 0 {
		sum.Clean++
	} else {
		sum.Mined++
	}
	sum.Created = append(sum.Created, res.Created...)
	sum.Reinforced = append(sum.Reinforced, res.Reinforced...)
	sum.Confirmed = append(sum.Confirmed, res.Confirmed...)
	sum.Retired = append(sum.Retired, res.Retired...)
	sum.Attributed += res.Attributed
	sum.StyleObs += res.StyleObs
	sum.Notes = append(sum.Notes, res.Notes...)
}

func (w *worker) mineSerial(ctx context.Context, jobs []queue.Job) {
	miner := w.newMiner(nil)
	for _, job := range jobs {
		cur, ok := w.prep(job)
		if !ok {
			continue
		}
		res, newCur, err := miner.MineSession(ctx, job, cur)
		if w.collect(job, res, newCur, err) {
			return
		}
	}
}

type mineTask struct {
	job queue.Job
	cur queue.Cursor
}

type mineOutcome struct {
	job    queue.Job
	res    mine.Result
	newCur queue.Cursor
	err    error
}

// mineParallel is the --no-cap drain: the LLM-heavy MineSession fans out
// across a bounded pool, single-writer store writes funnel through a shared
// WriteMu, and bookkeeping comes back here. Each task gets its own Miner —
// MineSession keeps a per-session vector cache on it, so sharing one would
// race; Store, Tier, Emb and WriteMu are safe to share.
func (w *worker) mineParallel(ctx context.Context, jobs []queue.Job) {
	var tasks []mineTask
	for _, job := range jobs {
		if cur, ok := w.prep(job); ok {
			tasks = append(tasks, mineTask{job, cur})
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan mineOutcome, len(tasks))
	// Dispatch concurrently with collect so a halt (cancel) actually stops the
	// remaining launches instead of racing them all out first.
	go w.dispatch(runCtx, tasks, &sync.Mutex{}, out)
	for o := range out {
		if w.collect(o.job, o.res, o.newCur, o.err) {
			cancel() // halt: stop remaining dispatch; in-flight jobs stay queued
		}
	}
}

// dispatch launches tasks with at most parallelDrainWorkers in flight and
// closes out when all have reported. Runs on its own goroutine; it touches
// no worker state besides the read-only miner fields.
func (w *worker) dispatch(ctx context.Context, tasks []mineTask, writeMu *sync.Mutex, out chan<- mineOutcome) {
	sem := make(chan struct{}, parallelDrainWorkers)
	var wg sync.WaitGroup
	for _, t := range tasks {
		if ctx.Err() != nil {
			break // halted — stop dispatching; remaining jobs stay queued
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t mineTask) {
			defer wg.Done()
			defer func() { <-sem }()
			out <- w.mineOne(ctx, t, writeMu)
		}(t)
	}
	wg.Wait()
	close(out)
}

func (w *worker) mineOne(ctx context.Context, t mineTask, writeMu *sync.Mutex) mineOutcome {
	if ctx.Err() != nil { // launched after a halt — don't spawn a call
		return mineOutcome{t.job, mine.Result{}, t.cur, ctx.Err()}
	}
	res, newCur, err := w.newMiner(writeMu).MineSession(ctx, t.job, t.cur)
	if ctx.Err() != nil {
		err = ctx.Err() // killed mid-call ⇒ leave queued, don't park
	}
	return mineOutcome{t.job, res, newCur, err}
}

// afterMining runs the pipelines that follow transcript mining: style
// synthesis and the branch pattern index (skipped after a cap or backend
// halt), then the zero-cost janitor, vectors for new cards, and the commit.
func (w *worker) afterMining(ctx context.Context) {
	if !w.sum.Capped && !w.sum.BackendDown {
		w.synthesizeStyle(ctx)
		w.indexPatterns(ctx)
	}
	w.retireStale(ctx)
	w.embedNew(ctx)
	w.commit()
}

// synthesizeStyle is pipeline B: policy-gated style synthesis over the
// observations ledger.
func (w *worker) synthesizeStyle(ctx context.Context) {
	synth := &style.Synthesizer{Base: w.base, Store: w.store, Tier: w.tier}
	sres, err := synth.Run(ctx, w.opts.ForceStyle, time.Now().UTC())
	w.sum.Style = sres
	w.sum.Usage.Add(sres.Usage)
	if err != nil && !errors.Is(err, llmtier.ErrCapped) {
		w.sum.Notes = append(w.sum.Notes, "style synthesis: "+firstNoteLine(err))
	}
}

// indexPatterns is pipeline D: the branch-tip pattern index over the
// configured repos. Comparing tips is milliseconds of git; unmoved repos cost
// nothing.
func (w *worker) indexPatterns(ctx context.Context) {
	pr := &patterns.Runner{Base: w.base, Store: w.store, Tier: w.tier, Logf: w.logf}
	pres, err := pr.Run(ctx, w.cfg.Repos)
	w.sum.Patterns = pres
	w.sum.Usage.Add(pres.Usage)
	if err != nil {
		w.sum.Notes = append(w.sum.Notes, "pattern index: "+firstNoteLine(err))
	}
}

// retireStale auto-retires mined candidates left unreinforced past the TTL.
// Zero-cost (local file ops, no backend), so it runs even after a cap hit.
// Only candidates are touched — dormant confirmed cards are reported by
// `culi stats`, never deleted (C4).
func (w *worker) retireStale(ctx context.Context) {
	kdir := config.KnowledgeDir(w.base)
	ttl := time.Duration(w.cfg.Learn.CandidateTTLDays) * 24 * time.Hour
	stale, err := mine.RetireStaleCandidates(ctx, w.store, kdir, ttl, time.Now().UTC())
	if err != nil {
		w.sum.Notes = append(w.sum.Notes, "candidate janitor: "+firstNoteLine(err))
		return
	}
	if len(stale) == 0 {
		return
	}
	w.sum.Retired = append(w.sum.Retired, stale...)
	w.sum.Notes = append(w.sum.Notes, fmt.Sprintf("%d stale candidate(s) auto-retired (>%dd unreinforced)",
		len(stale), w.cfg.Learn.CandidateTTLDays))
	if _, err := indexer.Sync(ctx, w.store, kdir); err != nil {
		w.sum.Notes = append(w.sum.Notes, "janitor reindex: "+firstNoteLine(err))
	}
}

// embedNew computes vectors for anything new, so the next run's dedup can use
// the embedding arm.
func (w *worker) embedNew(ctx context.Context) {
	sum := w.sum
	if len(sum.Created)+len(sum.Style.Created)+len(sum.Patterns.Created) == 0 && len(sum.Reinforced) == 0 {
		return
	}
	ectx, cancel := context.WithTimeout(ctx, embedBudget)
	defer cancel()
	_, _ = indexer.EmbedMissing(ectx, w.store, w.emb, w.cfg.Ollama.Model)
}

// commit records one aggregate governance commit per worker run
// (best-effort — history must never fail learning). A concurrent review or
// save_lesson commit may sweep this run's files under its own message; the
// change is still recorded, just labeled by the other actor — tolerated by
// design, do not add locking for it.
func (w *worker) commit() {
	msg := commitMessage(w.sum)
	if msg == "" {
		return
	}
	if err := knowledge.Commit(config.KnowledgeDir(w.base), msg); err != nil && w.logf != nil {
		w.logf("learn: knowledge commit: %v", err)
	}
}

// commitMessage summarizes one run's knowledge changes; "" when nothing
// changed on disk.
func commitMessage(sum Summary) string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(len(sum.Created), "mined candidates")
	add(len(sum.Reinforced), "reinforced")
	add(len(sum.Confirmed), "confirmed")
	add(len(sum.Retired), "retired")
	add(len(sum.Style.Created), "style candidates")
	add(len(sum.Style.Confirmed), "style confirmed")
	add(len(sum.Style.Retired), "style retired")
	add(len(sum.Patterns.Created), "patterns")
	add(len(sum.Patterns.Retired), "patterns retired")
	if len(parts) == 0 {
		return ""
	}
	return "learn: " + strings.Join(parts, ", ")
}

// firstNoteLine compresses an error chain into one note line.
func firstNoteLine(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
