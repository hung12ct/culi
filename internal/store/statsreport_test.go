package store

import (
	"context"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/harness"
)

func TestInjectionAggsAndSessionCount(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	recs := []InjectionRecord{
		{CardID: "a", Granularity: GranBody, Tokens: 100},
		{CardID: "b", Granularity: GranHook, Tokens: 15},
	}
	if err := s.RecordInjections(ctx, "s1", "user-prompt-submit", "h1", "", harness.Claude, recs); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "s2", "session-start", "", "", harness.Codex, recs[:1]); err != nil {
		t.Fatal(err)
	}

	n, err := s.SessionCount(ctx)
	if err != nil || n != 2 {
		t.Fatalf("sessions = %d, err %v", n, err)
	}
	injected, err := s.InjectedCardIDs(ctx)
	if err != nil || !injected["a"] || !injected["b"] || len(injected) != 2 {
		t.Fatalf("injected = %v, err %v", injected, err)
	}
	aggs, err := s.InjectionAggs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	total, tokens := 0, 0
	for _, a := range aggs {
		total += a.Count
		tokens += a.Tokens
	}
	if total != 3 || tokens != 215 {
		t.Errorf("aggs = %+v", aggs)
	}
}

func TestAllCardStatsDecays(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.AddFeedback(ctx, "cardx", FeedbackExpanded, 2); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stats, err := s.AllCardStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st := stats["cardx"]; st.Expanded < 1.99 || st.Expanded > 2.01 {
		t.Errorf("fresh expanded = %f", st.Expanded)
	}
	// Two half-lives later: quartered.
	later := now.Add(2 * utilityHalfLife)
	stats, err = s.AllCardStats(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if st := stats["cardx"]; st.Expanded < 0.45 || st.Expanded > 0.55 {
		t.Errorf("decayed expanded = %f", st.Expanded)
	}
}

func TestInjectionsSinceReturnsCompleteWindow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	rec := []InjectionRecord{{CardID: "a", Granularity: GranSummary, Tokens: 42}}
	if err := s.RecordInjections(ctx, "old", "user-prompt-submit", "", "/repo", harness.Claude, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "new", "user-prompt-submit", "", "/repo", harness.Codex, rec); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -8).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, "UPDATE injections SET ts = ? WHERE session_id = 'old'", old); err != nil {
		t.Fatal(err)
	}

	rows, err := s.InjectionsSince(ctx, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID != "new" {
		t.Fatalf("rows = %+v, want only new session", rows)
	}
}
