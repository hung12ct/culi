package pack

import (
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/retrieve"
)

// MMR: maximal marginal relevance keeps near-duplicate cards from burning two
// budget slots. λ balances relevance against novelty; 0.7 favors relevance.
const mmrLambda = 0.7

// mmrOrder greedily reorders candidates: pinned cards keep their position at
// the front, the rest are picked by λ·normScore − (1−λ)·maxSim(selected).
// Cards without vectors contribute sim 0 (no dedup signal — never penalized).
// O(n²) over ≤12 candidates: microseconds.
func mmrOrder(cands []retrieve.Candidate) []retrieve.Candidate {
	if len(cands) < 3 {
		return cands // nothing to deduplicate against
	}

	// Normalize relevance to [0,1] so it is comparable with cosine sim.
	maxScore := 0.0
	for _, c := range cands {
		if c.Score > maxScore {
			maxScore = c.Score
		}
	}
	if maxScore == 0 {
		return cands
	}

	out := make([]retrieve.Candidate, 0, len(cands))
	pool := make([]retrieve.Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Pinned {
			out = append(out, c) // pins bypass MMR: triggers promised placement
		} else {
			pool = append(pool, c)
		}
	}

	for len(pool) > 0 {
		bestIdx, bestVal := 0, -1.0
		for i, c := range pool {
			sim := 0.0
			if c.Vec != nil {
				for _, sel := range out {
					if s := embed.Dot(c.Vec, sel.Vec); s > sim {
						sim = s
					}
				}
			}
			val := mmrLambda*(c.Score/maxScore) - (1-mmrLambda)*sim
			if val > bestVal {
				bestIdx, bestVal = i, val
			}
		}
		out = append(out, pool[bestIdx])
		pool = append(pool[:bestIdx], pool[bestIdx+1:]...)
	}
	return out
}
