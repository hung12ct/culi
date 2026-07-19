package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/store"
)

// statuslineDeadline bounds the whole render: the statusline re-runs
// constantly, so a wedged store must degrade to an empty segment, never a
// hang.
const statuslineDeadline = 300 * time.Millisecond

// Statusline renders culi's segment for Claude Code's statusLine protocol:
// session JSON on stdin, one line on stdout. Same discipline as hooks (C1):
// any failure prints nothing and exits 0 — a broken statusline must never
// distract from a working session.
func Statusline(args []string) error {
	_ = args
	var in struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in)

	ctx, cancel := context.WithTimeout(context.Background(), statuslineDeadline)
	defer cancel()
	line, err := statuslineText(ctx, in.SessionID)
	if err != nil || line == "" {
		return nil // fail open, render nothing
	}
	fmt.Println(line)
	return nil
}

// statuslineText composes the segment. Read-only, few tiny queries.
func statuslineText(ctx context.Context, sessionID string) (string, error) {
	base, err := config.BaseDir()
	if err != nil {
		return "", err
	}
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return "", err
	}
	defer s.Close()

	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return "", err
	}
	live, candidates := 0, 0
	corpusTok := 0
	for _, c := range cards {
		switch c.Status {
		case "candidate":
			candidates++
		case "retired":
		default:
			live++
			corpusTok += c.TokBody
		}
	}

	parts := []string{fmt.Sprintf("culi %d cards", live)}
	if sessionID != "" {
		if tok, err := s.SessionTokens(ctx, sessionID); err == nil && tok > 0 {
			parts = append(parts, fmt.Sprintf("%d tok this session", tok))
		}
	}
	if sessions, err := s.SessionCount(ctx); err == nil && sessions > 0 && corpusTok > 0 {
		injected := 0
		if aggs, err := s.InjectionAggs(ctx); err == nil {
			for _, a := range aggs {
				// Same two buckets `culi stats` counts — the numbers must
				// never diverge between the two surfaces.
				if a.Event == "user-prompt-submit" || a.Event == "session-start" {
					injected += a.Tokens
				}
			}
		}
		if cf := computeCounterfactual(sessions, corpusTok, injected); cf.SavedPercent > 0 {
			parts = append(parts, fmt.Sprintf("saved ~%.0f%% (7d)", cf.SavedPercent))
		}
	}
	if candidates > 0 {
		parts = append(parts, fmt.Sprintf("%d to review", candidates))
	}
	if failed := countFailedJobs(base); failed > 0 {
		parts = append(parts, fmt.Sprintf("⚠ %d failed job(s)", failed))
	}
	return strings.Join(parts, " · "), nil
}

// countFailedJobs counts parked learn jobs — the loudest bug signal culi has.
func countFailedJobs(base string) int {
	entries, err := os.ReadDir(filepath.Join(config.InboxDir(base), "failed"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), ".json") {
			n++
		}
	}
	return n
}
