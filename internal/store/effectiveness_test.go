package store

import (
	"context"
	"testing"

	"github.com/hung12ct/culi/internal/harness"
)

func TestClassifyEffectiveness(t *testing.T) {
	cases := []struct {
		name  string
		cs    CardStats
		usage WindowUsage
		want  string
	}{
		{"never observed", CardStats{}, WindowUsage{}, EffUncertain},
		{"one quiet injection", CardStats{}, WindowUsage{Injections: 1, Tokens: 300}, EffUncertain},
		{"expanded card", CardStats{Expanded: 2}, WindowUsage{Injections: 10, Tokens: 500}, EffHelpful},
		{"referenced card", CardStats{Referenced: 1}, WindowUsage{Injections: 4, Tokens: 100}, EffHelpful},
		// One explicit downvote (weight 5) outweighs one expansion (weight 3).
		{"downvote beats one pull", CardStats{Expanded: 1, Downvoted: 1}, WindowUsage{Injections: 6, Tokens: 200}, EffNoisy},
		// Three abandoned-pointer penalties (0.1 each) with zero pulls → noisy.
		{"abandoned pointers", CardStats{Downvoted: 0.3}, WindowUsage{Injections: 5, Tokens: 100}, EffNoisy},
		// Well-pulled cards survive a downvote: 3·3 > 5·1.
		{"pulls outweigh a downvote", CardStats{Expanded: 3, Downvoted: 1}, WindowUsage{Injections: 20, Tokens: 400}, EffHelpful},
		// Signal-free but heavy: bodies are unobservable, so this is
		// "expensive", never "noisy". Exposure comes purely from the log —
		// the injected counter is never written in practice.
		{"quiet and heavy", CardStats{}, WindowUsage{Injections: 12, Tokens: 5000}, EffExpensive},
		{"quiet and cheap", CardStats{}, WindowUsage{Injections: 12, Tokens: 300}, EffUncertain},
		// A decayed counter alone (no window rows) still counts as exposure.
		{"counter-only exposure", CardStats{Injected: 8}, WindowUsage{Tokens: 5000}, EffExpensive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyEffectiveness(tc.cs, tc.usage)
			if got.Bucket != tc.want {
				t.Errorf("bucket = %s, want %s (%+v)", got.Bucket, tc.want, got)
			}
		})
	}
}

func TestClassifyEffectivenessRates(t *testing.T) {
	e := ClassifyEffectiveness(
		CardStats{Expanded: 2, Referenced: 1, Downvoted: 1},
		WindowUsage{Injections: 10})
	if e.PullRate < 0.29 || e.PullRate > 0.31 {
		t.Errorf("pull rate = %f, want 0.3", e.PullRate)
	}
	if e.Positive != 11 || e.Negative != 5 {
		t.Errorf("weighted signals = %f / %f, want 11 / 5", e.Positive, e.Negative)
	}
	// Lifetime pulls over a shrunken window can exceed 1 pull/injection;
	// the rate is clamped so the UI never shows >100%.
	if c := ClassifyEffectiveness(CardStats{Expanded: 9}, WindowUsage{Injections: 2}); c.PullRate != 1 {
		t.Errorf("clamped pull rate = %f, want 1", c.PullRate)
	}
}

func TestCardWindowUsage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	recs := []InjectionRecord{
		{CardID: "a", Granularity: GranBody, Tokens: 100},
		{CardID: "b", Granularity: GranHook, Tokens: 15},
	}
	if err := s.RecordInjections(ctx, "s1", "user-prompt-submit", "", "", harness.Claude, recs); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "s2", "session-start", "", "", harness.Codex, recs[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := s.CardWindowUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != (WindowUsage{Injections: 2, Tokens: 200}) || got["b"] != (WindowUsage{Injections: 1, Tokens: 15}) || len(got) != 2 {
		t.Errorf("window usage = %v", got)
	}
}
