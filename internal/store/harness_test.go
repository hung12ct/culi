package store

import (
	"context"
	"testing"

	"github.com/hung12ct/culi/internal/harness"
)

// TestInjectionHarnessRoundTrip proves the v3 harness column is written by
// RecordInjections and read back by RecentInjections — the authoritative
// attribution the review console's harness filter depends on.
func TestInjectionHarnessRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	rec := []InjectionRecord{{CardID: "a", Granularity: GranHook, Tokens: 10}}
	if err := s.RecordInjections(ctx, "claude:s1", "user-prompt-submit", "", "", harness.Claude, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "codex:s2", "session-start", "", "", harness.Codex, rec); err != nil {
		t.Fatal(err)
	}

	rows, err := s.RecentInjections(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.SessionID] = r.Harness
	}
	if got["claude:s1"] != harness.Claude.String() || got["codex:s2"] != harness.Codex.String() {
		t.Fatalf("harness not round-tripped: %v", got)
	}
}
