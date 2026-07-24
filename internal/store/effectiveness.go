package store

import (
	"context"
	"fmt"
)

// Effectiveness buckets — a per-card verdict derived from the decayed feedback
// counters plus the card's footprint in the injection-log retention window.
// Purely read-side: buckets never feed ranking (UtilityMultipliers does that);
// they exist so the operator can see which cards earn their tokens.
const (
	EffHelpful   = "helpful"   // pulled or referenced after injection
	EffNoisy     = "noisy"     // downvoted or repeatedly ignored as a pointer
	EffExpensive = "expensive" // heavy token spend, no observed positive signal
	EffUncertain = "uncertain" // too few observations to judge
)

// Feedback weights shared with UtilityMultipliers: a reference is worth more
// than an expansion; a downvote weighs as much as a reference.
const (
	wExpanded   = 3
	wReferenced = 5
	wDownvoted  = 5
)

// Classification thresholds. Below effMinObserved observed injections a card
// with no explicit signal stays "uncertain" — one quiet session proves
// nothing. effExpensiveTokens is the window token spend past which a signal-
// free card is flagged "expensive": ~2 full per-prompt budgets (700 tok) of
// weekly spend with nothing to show for it. A fixed threshold beats a
// percentile for explainability; tune here if corpora grow much hotter.
const (
	effMinObserved     = 3.0
	effExpensiveTokens = 1500
)

// WindowUsage is one card's footprint in the injection-log retention window
// (~7 days, bounded by PruneStale): how often it was delivered and what it
// cost. This — not CardStats.Injected — is the ground truth for exposure:
// no code path bumps the injected counter (the hook path stays write-light),
// so classification must read the log.
type WindowUsage struct {
	Injections int
	Tokens     int
}

// Effectiveness is one card's verdict with the rates behind it.
type Effectiveness struct {
	Bucket   string  `json:"bucket"`
	Positive float64 `json:"positive"`  // weighted: 3·expanded + 5·referenced
	Negative float64 `json:"negative"`  // weighted: 5·downvoted
	PullRate float64 `json:"pull_rate"` // (expanded+referenced) per observed injection
}

// ClassifyEffectiveness buckets one card from its decayed feedback counters
// and its retention-window usage.
//
// Deliberate asymmetry: full-body pushes are unobservable (plan §9 — an
// injected body can help without ever being expanded), so "no signal" alone
// never marks a card noisy. Negative inference comes only from explicit
// downvotes and the abandoned-pointer penalty, both already in Downvoted;
// signal-free cards are at most "expensive" when their token cost is high.
func ClassifyEffectiveness(cs CardStats, usage WindowUsage) Effectiveness {
	e := Effectiveness{
		Positive: wExpanded*cs.Expanded + wReferenced*cs.Referenced,
		Negative: wDownvoted * cs.Downvoted,
	}
	// The decayed counter would be the better exposure measure if anything
	// wrote it; take whichever source saw more, so hypothetical counters
	// still count and the empty-counter reality still classifies.
	observed := max(cs.Injected, float64(usage.Injections))
	if observed > 0 {
		e.PullRate = min(1, (cs.Expanded+cs.Referenced)/observed)
	}
	switch {
	case e.Negative > e.Positive && e.Negative > 0:
		e.Bucket = EffNoisy
	case e.Positive > 0:
		e.Bucket = EffHelpful
	case observed < effMinObserved:
		e.Bucket = EffUncertain
	case usage.Tokens >= effExpensiveTokens:
		e.Bucket = EffExpensive
	default:
		e.Bucket = EffUncertain
	}
	return e
}

// CardWindowUsage aggregates the injection log per card over its retention
// window — the exposure and cost half of the effectiveness verdict. Off the
// hot path.
func (s *Store) CardWindowUsage(ctx context.Context) (map[string]WindowUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT card_id, COUNT(*), COALESCE(SUM(tokens), 0) FROM injections GROUP BY card_id")
	if err != nil {
		return nil, fmt.Errorf("store: aggregating card window usage: %w", err)
	}
	defer rows.Close()
	out := map[string]WindowUsage{}
	for rows.Next() {
		var id string
		var u WindowUsage
		if err := rows.Scan(&id, &u.Injections, &u.Tokens); err != nil {
			return nil, fmt.Errorf("store: scanning card window usage: %w", err)
		}
		out[id] = u
	}
	return out, rows.Err()
}
