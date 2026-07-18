package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/pack"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// handle dispatches one event and returns the additionalContext ("" = inject
// nothing). Errors bubble to Run's fail-open wrapper.
func handle(ctx context.Context, base, event string, in Input) (string, error) {
	switch event {
	case "user-prompt-submit":
		return handlePrompt(ctx, base, in)
	case "session-start":
		return handleSessionStart(ctx, base, in)
	case "session-end":
		return "", handleSessionEnd(base, in)
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
	r := &retrieve.Retriever{Store: s}
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

// handleSessionEnd enqueues the transcript for the Phase 4 learn worker —
// cheap file write, no LLM, no parsing (learning never runs in a hook).
func handleSessionEnd(base string, in Input) error {
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
	return nil
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
