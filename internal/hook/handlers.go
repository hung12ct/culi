package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/pack"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// sessionStartEmbedBudget bounds the inline embedding pass at SessionStart:
// enough for a handful of changed cards, far under the outer hook timeout.
// The per-prompt path never embeds cards — only the query (100ms, in
// retrieve).
const sessionStartEmbedBudget = 2 * time.Second

// handle dispatches one event and returns the additionalContext ("" = inject
// nothing). Errors bubble to Run's fail-open wrapper.
func handle(ctx context.Context, base, event string, in Input) (string, error) {
	switch event {
	case "user-prompt-submit":
		return handlePrompt(ctx, base, in)
	case "session-start":
		return handleSessionStart(ctx, base, in)
	case "session-end":
		return "", handleSessionEnd(ctx, base, in)
	}
	return "", fmt.Errorf("hook: unhandled event %q", event)
}

func handlePrompt(ctx context.Context, base string, in Input) (string, error) {
	cfg, err := config.Load(base)
	if err != nil {
		return "", err
	}
	retrieve.ExtendLexicon(cfg.ExtraAcks, cfg.ExtraStopwords)
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return "", err
	}
	defer s.Close()

	lastPrompt, err := s.SwapLastPrompt(ctx, in.SessionID, in.Prompt)
	if err != nil {
		return "", err
	}
	gate := retrieve.Gate(in.Prompt, lastPrompt)
	if gate.Skip {
		logf(base, "gate skip (%s) session=%s", gate.Reason, in.SessionID)
		return "", nil
	}

	sc := retrieve.DetectScope(in.CWD)
	r := &retrieve.Retriever{
		Store:    s,
		Embedder: embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model),
		Model:    cfg.Ollama.Model,
	}
	cands, err := r.Retrieve(ctx, gate.Query, sc)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", nil
	}

	injected, err := s.InjectedLevels(ctx, in.SessionID)
	if err != nil {
		return "", err
	}
	inj, err := pack.Pack(cands, injected, cfg.PushBudget, bodyLoader(ctx, s))
	if err != nil {
		return "", err
	}
	if len(inj.Items) == 0 {
		return "", nil
	}
	if err := s.RecordInjections(ctx, in.SessionID, "user-prompt-submit", promptHash(in.Prompt), inj.Records()); err != nil {
		return "", err
	}
	return inj.Render(), nil
}

func handleSessionStart(ctx context.Context, base string, in Input) (string, error) {
	cfg, err := config.Load(base)
	if err != nil {
		return "", err
	}
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return "", err
	}
	defer s.Close()

	// Compact rebuilt the context window: prior injections are gone from it.
	if in.Source == "compact" {
		if err := s.ResetSession(ctx, in.SessionID); err != nil {
			return "", err
		}
	}

	// Inline incremental reindex — off the per-prompt path, usually a no-op.
	if _, err := indexer.Sync(ctx, s, config.KnowledgeDir(base)); err != nil {
		return "", err
	}
	// Vectors for new/changed cards, bounded and best-effort: a dead Ollama
	// must never delay session start (C1) — BM25 covers the gap.
	ectx, cancel := context.WithTimeout(ctx, sessionStartEmbedBudget)
	e := embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model)
	if _, err := indexer.EmbedMissing(ectx, s, e, cfg.Ollama.Model); err != nil {
		logf(base, "session-start: embed: %v", err) // non-fatal
	}
	cancel()
	// Bound the disposable history while we're here (7-day retention).
	if err := s.PruneStale(ctx, 7); err != nil {
		logf(base, "session-start: prune: %v", err) // non-fatal
	}

	// Resume keeps dedup state; re-injecting the baseline would duplicate.
	if in.Source == "resume" {
		return "", nil
	}

	sc := retrieve.DetectScope(in.CWD)
	r := &retrieve.Retriever{Store: s}
	cands, err := r.Baseline(ctx, sc)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", nil
	}
	injected, err := s.InjectedLevels(ctx, in.SessionID)
	if err != nil {
		return "", err
	}
	inj, err := pack.Pack(cands, injected, cfg.BaselineBudget, bodyLoader(ctx, s))
	if err != nil {
		return "", err
	}
	if len(inj.Items) == 0 {
		return "", nil
	}
	if err := s.RecordInjections(ctx, in.SessionID, "session-start", "", inj.Records()); err != nil {
		return "", err
	}
	return inj.Render(), nil
}

// handleSessionEnd folds utility feedback (pointers injected but never
// expanded) and enqueues the transcript for the Phase 4 learn worker — cheap
// writes, no LLM, no parsing (learning never runs in a hook).
func handleSessionEnd(ctx context.Context, base string, in Input) error {
	if in.SessionID != "" {
		if s, err := store.Open(ctx, config.DBPath(base)); err == nil {
			if err := s.PenalizeAbandonedPointers(ctx, in.SessionID); err != nil {
				logf(base, "session-end: pointer penalty: %v", err) // non-fatal
			}
			s.Close()
		}
	}
	if in.TranscriptPath == "" {
		return nil
	}
	dir := config.InboxDir(base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hook: creating inbox: %w", err)
	}
	job := map[string]string{
		"session_id":      in.SessionID,
		"transcript_path": in.TranscriptPath,
		"cwd":             in.CWD,
		"enqueued_at":     time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("hook: marshaling job: %w", err)
	}
	name := filepath.Join(dir, promptHash(in.SessionID+in.TranscriptPath)+".json")
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		return fmt.Errorf("hook: writing job: %w", err)
	}
	spawnLearnWorker(base)
	return nil
}

// spawnLearnWorker starts `culi learn --auto` detached: mining never runs in
// a hook (plan §learning A) and the worker self-limits (lockfile, throttle,
// spend caps). Best-effort — a spawn failure only delays learning until the
// next session end. CULI_NO_LEARN_SPAWN disables the spawn entirely: users
// who prefer manual `culi learn`, and tests, where os.Executable is the test
// binary and re-executing it would fork the whole suite.
func spawnLearnWorker(base string) {
	if os.Getenv("CULI_NO_LEARN_SPAWN") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		logf(base, "session-end: resolving executable: %v", err)
		return
	}
	cmd := exec.Command(exe, "learn", "--auto")
	detach(cmd) // own session: survives the hook's process group teardown
	if err := cmd.Start(); err != nil {
		logf(base, "session-end: spawning learn worker: %v", err)
		return
	}
	_ = cmd.Process.Release()
}

func bodyLoader(ctx context.Context, s *store.Store) pack.BodyLoader {
	return func(rowids []int64) (map[int64]string, error) {
		cards, err := s.CardsByRowid(ctx, rowids)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]string, len(cards))
		for _, c := range cards {
			out[c.Rowid] = c.Body
		}
		return out, nil
	}
}

func promptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
