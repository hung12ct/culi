// Package mine implements learning pipeline A: deterministic signal windows
// from one session's transcript → a single cheap structured call (escalated
// once on failure) → deduplicated candidate cards with a reinforcement
// lifecycle. Clean sessions cost zero model calls; every model call is capped
// by llmtier's ledger.
package mine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/learn/transcript"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// maxWindowBytes caps the rendered windows fed to the model — the cost lever
// that makes transcript size irrelevant.
const maxWindowBytes = 15 << 10

// Miner mines one session at a time. Fields are dependencies, wired by the
// worker.
type Miner struct {
	Base     string // culi home
	Store    *store.Store
	Tier     *llmtier.Tier
	Emb      embed.Embedder // nil ⇒ lexical dedup only
	EmbModel string
	Logf     func(format string, args ...any) // optional diagnostics
	// WriteMu, when set, serializes the store-writing tail (apply + reindex)
	// across concurrent MineSession calls — the index is single-writer (WAL),
	// so a --no-cap parallel drain fans out the LLM calls but funnels writes.
	WriteMu *sync.Mutex

	vecs map[int64][]float32 // lazy per-run cache of card embeddings
}

// Result summarizes one mined session.
type Result struct {
	Windows    int
	Created    []string // new candidate card IDs
	Reinforced []string // existing card IDs bumped
	Confirmed  []string // cards that crossed the 2-observation gate
	Retired    []string // superseded cards retired on confirm
	StyleObs   int
	Notes      []string
	Usage      llmgen.Usage
	Escalated  bool
}

// MineSession processes one queued job from its cursor and returns the new
// cursor. Zero windows ⇒ zero LLM calls (the common, free case). An ErrCapped
// from the tier is returned as-is so the worker stops and keeps the job.
func (m *Miner) MineSession(ctx context.Context, job queue.Job, cur queue.Cursor) (Result, queue.Cursor, error) {
	var res Result
	m.vecs = nil // card vectors may have changed since the previous session
	entries, newOff, err := transcript.ReadFrom(job.TranscriptPath, cur.Offset)
	if err != nil {
		return res, cur, fmt.Errorf("mine: %w", err)
	}
	next := queue.Cursor{Offset: newOff, MinedAt: time.Now().UTC()}
	if isSelfMiningTranscript(entries) {
		// One of culi's own `claude -p` mining calls, logged as a session and
		// enqueued (belt-and-suspenders for the config.InternalEnv hook guard).
		// Advance the cursor and consume the job without a model call.
		res.Notes = append(res.Notes, "skipped culi's own mining call (self-ingestion guard)")
		return res, next, nil
	}
	wins := transcript.Extract(entries)
	res.Windows = len(wins)
	if len(wins) == 0 {
		return res, next, nil // clean session: free
	}
	if m.Tier == nil {
		return res, cur, errors.New("mine: no learning backend")
	}

	repo := retrieve.DetectScope(job.CWD).Repo
	system := systemPrompt(repo, m.existingLessonLines(ctx))
	user := transcript.Render(entries, wins, maxWindowBytes)

	var out mineOut
	usage, err := m.Tier.Generate(ctx, false, system, user, "transcript_mine", mineSchema(), &out)
	res.Usage.Add(usage)
	if err != nil {
		if llmtier.IsStop(err) {
			return res, cur, err // capped or backend down — don't burn a strong-tier retry
		}
		// Escalate once to the strong tier (plan §learning A): schema failures
		// on small models are the expected failure mode.
		res.Escalated = true
		out = mineOut{}
		usage, err = m.Tier.Generate(ctx, true, system, user, "transcript_mine", mineSchema(), &out)
		res.Usage.Add(usage)
		if err != nil {
			return res, cur, fmt.Errorf("mine: %w", err)
		}
	}
	if out.Notes != "" {
		res.Notes = append(res.Notes, "model: "+firstLine(out.Notes))
	}

	// apply + reindex touch the single-writer index; serialize them when a
	// parallel drain shares this miner (WriteMu). The LLM call above already ran
	// lock-free. One sync picks up everything written; candidates index with
	// status=candidate and stay out of retrieval until confirmed.
	werr := func() error {
		if m.WriteMu != nil {
			m.WriteMu.Lock()
			defer m.WriteMu.Unlock()
		}
		if err := m.apply(ctx, job, repo, out, &res); err != nil {
			return err
		}
		_, err := indexer.Sync(ctx, m.Store, config.KnowledgeDir(m.Base))
		return err
	}()
	if werr != nil {
		return res, cur, werr
	}
	return res, next, nil
}

// apply lands the model output: dedup, lifecycle, card writes, style ledger.
func (m *Miner) apply(ctx context.Context, job queue.Job, repo string, out mineOut, res *Result) error {
	for _, group := range []struct {
		typ   string
		cards []cardOut
		cap   int
	}{
		{"lesson", out.Lessons, maxLessons},
		{"rule", out.MissingRules, maxRules},
	} {
		if len(group.cards) > group.cap {
			group.cards = group.cards[:group.cap]
		}
		for _, c := range group.cards {
			if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Markdown) == "" ||
				c.Confidence < minConfidence {
				continue
			}
			if err := m.applyCard(ctx, group.typ, c, repo, res); err != nil {
				return err
			}
		}
	}

	if len(out.StyleObservations) > maxStyleObs {
		out.StyleObservations = out.StyleObservations[:maxStyleObs]
	}
	n, err := appendStyleObservations(config.StateDir(m.Base), job, repo, out.StyleObservations)
	if err != nil {
		return err
	}
	res.StyleObs = n
	return nil
}

// applyCard routes one candidate through dedup and the lifecycle.
func (m *Miner) applyCard(ctx context.Context, typ string, c cardOut, repo string, res *Result) error {
	match, verdict := m.findMatch(ctx, c, typ)
	switch verdict {
	case matchDup:
		return m.reinforce(ctx, match, c, res)
	case matchNear:
		// Conservative middle band (deviation from the plan's judge call —
		// documented): probably the same insight phrased differently; creating
		// a card risks a duplicate, so drop it and say so. Re-learning will
		// either match cleanly next time or diverge into its own card.
		res.Notes = append(res.Notes, fmt.Sprintf("skipped near-duplicate of %s: %s", match.ID, firstLine(c.Title)))
		return nil
	}
	id, err := m.writeCandidate(ctx, typ, c, repo)
	if err != nil {
		return err
	}
	res.Created = append(res.Created, id)
	return nil
}

// existingLessonLines renders the 5 newest confirmed lessons as hook lines
// for the prompt's supersedes/dedup context.
func (m *Miner) existingLessonLines(ctx context.Context) string {
	cards, err := m.Store.AllCardsMeta(ctx)
	if err != nil {
		return ""
	}
	var lessons []store.StoredCard
	for _, c := range cards {
		if c.Type == "lesson" && c.Status != "candidate" && c.Status != "retired" {
			lessons = append(lessons, c)
		}
	}
	sort.Slice(lessons, func(a, b int) bool { return lessons[a].Rowid > lessons[b].Rowid })
	if len(lessons) > 5 {
		lessons = lessons[:5]
	}
	var b strings.Builder
	for _, l := range lessons {
		fmt.Fprintf(&b, "- %s: %s — %s\n", l.ID, l.Title, firstLine(l.Summary))
	}
	return b.String()
}

func (m *Miner) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// isSelfMiningTranscript reports whether entries are culi's own headless mining
// call (its user prompt opens with the mine system prompt). Cheap string check
// over already-parsed entries — no extra I/O.
func isSelfMiningTranscript(entries []transcript.Entry) bool {
	for _, e := range entries {
		if e.Role == "user" && strings.HasPrefix(strings.TrimSpace(e.Text), selfMineSentinel) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		cut := 120
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut-- // never split a rune (Vietnamese evidence)
		}
		s = s[:cut]
	}
	return strings.TrimSpace(s)
}
