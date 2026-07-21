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

	// --no-cap: drain the whole backlog in one run. Zero caps disable the gate
	// (see ledger.Allow), so this deliberately bypasses daily_usd_cap /
	// daily_call_cap for this run only (cfg is our local copy).
	if opts.IgnoreCaps {
		cfg.Learn.DailyUSDCap = 0
		cfg.Learn.DailyCallCap = 0
		sum.Notes = append(sum.Notes, "caps ignored (--no-cap): draining the full backlog")
	} else if lim := cfg.Learn.MaxJobsPerRun; lim > 0 && len(jobs) > lim {
		// Mine only the newest N this run (jobs are newest-first); the rest wait
		// for the next run or a --no-cap drain.
		sum.Notes = append(sum.Notes, fmt.Sprintf("this run: %d newest of %d queued (learn.max_jobs_per_run; --no-cap for all)", lim, len(jobs)))
		jobs = jobs[:lim]
	}

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

	e := embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model)
	miner := &mine.Miner{
		Base: base, Store: s, Tier: tier,
		Emb: e, EmbModel: cfg.Ollama.Model, Logf: logf,
	}
	cursors := queue.LoadCursors(stateDir)
	now := time.Now().UTC()
	fromStart := opts.FromStart

	// prep filters a job to a mineable (cursor, ok); ok=false means nothing new
	// (already marked Done). Runs in the main goroutine only.
	prep := func(job queue.Job) (queue.Cursor, bool) {
		st, err := os.Stat(job.TranscriptPath)
		if err != nil {
			sum.Notes = append(sum.Notes, fmt.Sprintf("transcript gone: %s", job.TranscriptPath))
			_ = queue.Done(job)
			return queue.Cursor{}, false
		}
		cur := cursors.Get(job.TranscriptPath)
		if fromStart {
			cur = queue.Cursor{}
		}
		if !queue.ShouldMine(cur, st.Size(), true, now) {
			_ = queue.Done(job) // nothing new since the last mine
			return queue.Cursor{}, false
		}
		return cur, true
	}

	// collect folds one finished mine into the summary and advances the cursor +
	// queue. Always runs in the main goroutine, so sum/cursors need no locks.
	// stop=true halts the run (caps/backend) leaving remaining jobs queued.
	collect := func(job queue.Job, res mine.Result, newCur queue.Cursor, err error) (stop bool) {
		sum.Usage.Add(res.Usage)
		switch {
		case errors.Is(err, llmtier.ErrCapped):
			sum.Capped = true
			sum.Notes = append(sum.Notes, firstNoteLine(err))
			return true
		case errors.Is(err, llmtier.ErrBackendUnavailable):
			sum.BackendDown = true
			sum.Notes = append(sum.Notes, firstNoteLine(err))
			return true
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return false // run halted/timed out — leave the job queued, don't park
		case err != nil:
			parked, ferr := queue.Fail(job)
			note := fmt.Sprintf("job %s failed: %v", job.SessionID, err)
			if parked {
				note += " (moved to inbox/failed)"
			}
			if ferr != nil {
				note += " (" + ferr.Error() + ")"
			}
			sum.Notes = append(sum.Notes, note)
			return false
		}
		// Parse-gated deletion: only after the result landed do cursor + job
		// advance, in that order — a crash between the two re-mines (dedup
		// absorbs it) rather than losing a region.
		cursors.Set(job.TranscriptPath, newCur)
		if err := cursors.Save(); err != nil {
			sum.Notes = append(sum.Notes, err.Error())
		}
		_ = queue.Done(job)
		if res.Windows == 0 {
			sum.Clean++
		} else {
			sum.Mined++
		}
		sum.Created = append(sum.Created, res.Created...)
		sum.Reinforced = append(sum.Reinforced, res.Reinforced...)
		sum.Confirmed = append(sum.Confirmed, res.Confirmed...)
		sum.Retired = append(sum.Retired, res.Retired...)
		sum.StyleObs += res.StyleObs
		sum.Notes = append(sum.Notes, res.Notes...)
		return false
	}

	if opts.IgnoreCaps && len(jobs) > 1 {
		// --no-cap parallel drain: fan out the LLM-heavy MineSession across a
		// bounded pool, funnel the single-writer store writes through a shared
		// WriteMu, and collect bookkeeping back here. Only worthwhile in bulk.
		// Each worker gets its OWN Miner — MineSession keeps a per-session vec
		// cache on the Miner, so sharing one would race; Store/Tier/Emb/WriteMu
		// are concurrency-safe and shared.
		writeMu := &sync.Mutex{}
		newMiner := func() *mine.Miner {
			return &mine.Miner{Base: base, Store: s, Tier: tier, Emb: e, EmbModel: cfg.Ollama.Model, Logf: logf, WriteMu: writeMu}
		}
		type task struct {
			job queue.Job
			cur queue.Cursor
		}
		var tasks []task
		for _, job := range jobs {
			if cur, ok := prep(job); ok {
				tasks = append(tasks, task{job, cur})
			}
		}
		type outcome struct {
			job    queue.Job
			res    mine.Result
			newCur queue.Cursor
			err    error
		}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		sem := make(chan struct{}, parallelDrainWorkers)
		out := make(chan outcome, len(tasks))
		var wg sync.WaitGroup
		// Dispatch concurrently with collect so a halt (cancel) actually stops
		// the remaining launches instead of racing them all out first.
		go func() {
			for _, t := range tasks {
				if runCtx.Err() != nil {
					break // halted — stop dispatching; remaining jobs stay queued
				}
				wg.Add(1)
				sem <- struct{}{} // bound to parallelDrainWorkers in flight
				go func(t task) {
					defer wg.Done()
					defer func() { <-sem }()
					if runCtx.Err() != nil { // launched after a halt — don't spawn a call
						out <- outcome{t.job, mine.Result{}, t.cur, runCtx.Err()}
						return
					}
					res, newCur, err := newMiner().MineSession(runCtx, t.job, t.cur)
					if runCtx.Err() != nil {
						err = runCtx.Err() // killed mid-call ⇒ leave queued, don't park
					}
					out <- outcome{t.job, res, newCur, err}
				}(t)
			}
			wg.Wait()
			close(out)
		}()
		for o := range out {
			if collect(o.job, o.res, o.newCur, o.err) {
				cancel() // halt: stop remaining dispatch; in-flight jobs stay queued
			}
		}
	} else {
		for _, job := range jobs {
			cur, ok := prep(job)
			if !ok {
				continue
			}
			res, newCur, err := miner.MineSession(ctx, job, cur)
			if collect(job, res, newCur, err) {
				break
			}
		}
	}

	// Pipeline B: policy-gated style synthesis over the observations ledger.
	// Skipped when the mining loop already hit the caps; its own call passes
	// the same ledger either way.
	if !sum.Capped && !sum.BackendDown {
		synth := &style.Synthesizer{Base: base, Store: s, Tier: tier}
		sres, err := synth.Run(ctx, opts.ForceStyle, time.Now().UTC())
		sum.Style = sres
		sum.Usage.Add(sres.Usage)
		if err != nil && !errors.Is(err, llmtier.ErrCapped) {
			sum.Notes = append(sum.Notes, "style synthesis: "+firstNoteLine(err))
		}
	}

	// Pipeline D: branch-tip pattern index over the configured repos. Tips
	// comparison is milliseconds of git; unmoved repos cost nothing.
	if !sum.Capped && !sum.BackendDown {
		pr := &patterns.Runner{Base: base, Store: s, Tier: tier, Logf: logf}
		pres, err := pr.Run(ctx, cfg.Repos)
		sum.Patterns = pres
		sum.Usage.Add(pres.Usage)
		if err != nil {
			sum.Notes = append(sum.Notes, "pattern index: "+firstNoteLine(err))
		}
	}

	// Janitor: auto-retire mined candidates left unreinforced past the TTL.
	// Zero-cost (local file ops, no backend), so it runs even after a cap hit.
	// Only candidates are touched — dormant confirmed cards are reported by
	// `culi stats`, never deleted (C4).
	kdir := config.KnowledgeDir(base)
	ttl := time.Duration(cfg.Learn.CandidateTTLDays) * 24 * time.Hour
	if stale, err := mine.RetireStaleCandidates(ctx, s, kdir, ttl, time.Now().UTC()); err != nil {
		sum.Notes = append(sum.Notes, "candidate janitor: "+firstNoteLine(err))
	} else if len(stale) > 0 {
		sum.Retired = append(sum.Retired, stale...)
		sum.Notes = append(sum.Notes, fmt.Sprintf("%d stale candidate(s) auto-retired (>%dd unreinforced)",
			len(stale), cfg.Learn.CandidateTTLDays))
		if _, err := indexer.Sync(ctx, s, kdir); err != nil {
			sum.Notes = append(sum.Notes, "janitor reindex: "+firstNoteLine(err))
		}
	}

	// Vectors for anything new, so next run's dedup can use the embed arm.
	if len(sum.Created)+len(sum.Style.Created)+len(sum.Patterns.Created) > 0 || len(sum.Reinforced) > 0 {
		ectx, cancel := context.WithTimeout(ctx, embedBudget)
		_, _ = indexer.EmbedMissing(ectx, s, e, cfg.Ollama.Model)
		cancel()
	}
	// Governance trail: one aggregate commit per worker run (best-effort —
	// history must never fail learning). A concurrent review/save_lesson
	// commit may sweep this run's files under its own message; the change is
	// still recorded, just labeled by the other actor — tolerated by design,
	// do not add locking for it.
	if msg := commitMessage(sum); msg != "" {
		if err := knowledge.Commit(config.KnowledgeDir(base), msg); err != nil && logf != nil {
			logf("learn: knowledge commit: %v", err)
		}
	}
	return sum, nil
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
