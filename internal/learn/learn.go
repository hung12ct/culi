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

// Summary reports one worker run.
type Summary struct {
	Backend    string
	Jobs       int // jobs seen
	Mined      int // sessions that produced windows and a model call
	Clean      int // sessions with zero windows (free)
	Created    []string
	Reinforced []string
	Confirmed  []string
	Retired    []string
	StyleObs   int
	Style      style.Result    // pipeline B synthesis, when it fired
	Patterns   patterns.Result // pipeline D branch index
	Usage      llmgen.Usage
	Notes      []string
	Capped     bool
}

// Options tunes one worker run.
type Options struct {
	FromStart  bool // ignore cursors, re-mine transcripts from the beginning
	ForceStyle bool // bypass the style-synthesis trigger policy
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

	for _, job := range jobs {
		st, err := os.Stat(job.TranscriptPath)
		if err != nil {
			sum.Notes = append(sum.Notes, fmt.Sprintf("transcript gone: %s", job.TranscriptPath))
			_ = queue.Done(job)
			continue
		}
		cur := cursors.Get(job.TranscriptPath)
		if fromStart {
			cur = queue.Cursor{}
		}
		if !queue.ShouldMine(cur, st.Size(), true, now) {
			_ = queue.Done(job) // nothing new since the last mine
			continue
		}

		res, newCur, err := miner.MineSession(ctx, job, cur)
		sum.Usage.Add(res.Usage)
		if errors.Is(err, llmtier.ErrCapped) {
			sum.Capped = true
			sum.Notes = append(sum.Notes, firstNoteLine(err))
			break // keep this job and the rest queued
		}
		if err != nil {
			parked, ferr := queue.Fail(job)
			note := fmt.Sprintf("job %s failed: %v", job.SessionID, err)
			if parked {
				note += " (moved to inbox/failed)"
			}
			if ferr != nil {
				note += " (" + ferr.Error() + ")"
			}
			sum.Notes = append(sum.Notes, note)
			continue
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
	}

	// Pipeline B: policy-gated style synthesis over the observations ledger.
	// Skipped when the mining loop already hit the caps; its own call passes
	// the same ledger either way.
	if !sum.Capped {
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
	if !sum.Capped {
		pr := &patterns.Runner{Base: base, Store: s, Tier: tier, Logf: logf}
		pres, err := pr.Run(ctx, cfg.Repos)
		sum.Patterns = pres
		sum.Usage.Add(pres.Usage)
		if err != nil {
			sum.Notes = append(sum.Notes, "pattern index: "+firstNoteLine(err))
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
