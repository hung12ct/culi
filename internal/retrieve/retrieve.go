// Package retrieve implements the token-minimal retrieval funnel:
// gate → scope filter + shadowing → trigger pinning →
// (BM25 ‖ embed+cosine) → RRF fuse × utility → floor.
// The embedding arm is optional at every level: nil Embedder, open breaker,
// or a dead Ollama all collapse the fusion to BM25-only.
package retrieve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/store"
)

// Funnel tuning. rrfK is the standard reciprocal-rank-fusion constant; with a
// single BM25 list it just maps rank→score monotonically, and the Phase 3
// cosine arm adds its own 1/(k+rank) term without rescaling anything.
const (
	rrfK          = 60.0
	bm25Depth     = 50
	maxCandidates = 12
	maxPins       = 2
	pinFallback   = 0.01 // bonus for trigger hits beyond the pin cap
)

// Candidate is one card that survived the funnel, best first. Vec is the
// card's embedding when the cosine arm ran (nil otherwise) — the packer uses
// it for MMR dedup.
type Candidate struct {
	Card   store.StoredCard
	Score  float64
	Pinned bool
	Vec    []float32
}

// Retriever runs the funnel against a store. Embedder nil ⇒ BM25-only.
type Retriever struct {
	Store    *store.Store
	Embedder embed.Embedder
	Model    string // embedding model tag; vectors from other models are stale
}

// Retrieve returns ranked candidates for a gated query. May return an empty
// slice — the score floor decides that injecting nothing is correct.
func (r *Retriever) Retrieve(ctx context.Context, query string, sc Scope) ([]Candidate, error) {
	queryTerms := terms(query, maxQueryTerms)
	matchExpr := FTSExpr(query)

	// Embedding arm runs concurrently with everything below (~1 HTTP call vs
	// metadata scan + FTS query).
	armCh := r.startEmbedArm(ctx, query)

	// Load card metadata (no bodies — packer hydrates the few it needs).
	metas, err := r.Store.AllCardsMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}

	// Scope filter + key shadowing, in Go: corpus is small and this keeps SQL
	// trivial. Shadowing: among in-scope cards sharing a key, only the
	// narrowest scope survives.
	inScope := make([]store.StoredCard, 0, len(metas))
	bestKeyRank := map[string]int{}
	for _, c := range metas {
		if c.Status == "candidate" || c.Status == "retired" {
			continue // learning lifecycle: only confirmed cards inject (Phase 4 nuance)
		}
		rank := c.NarrowestScopeRank(sc.Allowed)
		if rank < 0 {
			continue
		}
		inScope = append(inScope, c)
		if c.Key != "" && rank > bestKeyRank[c.Key] {
			bestKeyRank[c.Key] = rank
		}
	}

	// BM25 arm.
	bm25Rank := map[int64]int{} // rowid → 1-based rank
	if matchExpr != "" {
		hits, err := r.Store.SearchBM25(ctx, matchExpr, bm25Depth)
		if err != nil {
			return nil, fmt.Errorf("retrieve: %w", err)
		}
		for i, h := range hits {
			bm25Rank[h.Rowid] = i + 1
		}
	}

	// Join the embedding arm and rank in-scope cards by cosine.
	a := <-armCh
	cosRank, cosSim := cosineRanks(a, inScope)

	// Utility multipliers tune ordering from decayed feedback; missing = 1.0.
	utils, err := r.Store.UtilityMultipliers(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}

	foldedQuery := fold(query)
	var out []Candidate
	for _, c := range inScope {
		rank := c.NarrowestScopeRank(sc.Allowed)
		if c.Key != "" && rank < bestKeyRank[c.Key] {
			continue // shadowed by a narrower-scope card with the same key
		}

		pinned := matchesTrigger(c.Triggers.Keywords, foldedQuery)

		// RRF: each arm contributes 1/(k+rank); rank-based, so BM25 and cosine
		// never need scale normalization.
		score := 0.0
		br, bm25Hit := bm25Rank[c.Rowid]
		if bm25Hit {
			score += 1.0 / (rrfK + float64(br))
		}
		if cr, ok := cosRank[c.Rowid]; ok {
			score += 1.0 / (rrfK + float64(cr))
		}
		if score == 0 && !pinned {
			continue
		}

		// Score floor: a candidate needs a pin, a strong semantic match, or a
		// lexical match of ≥2 terms (all terms of a 1-term query) — single
		// stray word overlaps inject nothing.
		lexicalPass := bm25Hit && matchedTerms(c, queryTerms) >= min(2, len(queryTerms))
		if !pinned && cosSim[c.Rowid] < cosFloor && !lexicalPass {
			continue
		}

		if u, ok := utils[c.ID]; ok {
			score *= u
		}
		score += sc.Boost(rank)
		out = append(out, Candidate{Card: c, Score: score, Pinned: pinned, Vec: a.vecs[c.Rowid]})
	}

	out = capPins(out)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out, nil
}

// capPins sorts candidates (pins first, then score) and demotes trigger hits
// beyond maxPins to a plain ranking bonus — pins are capped by SCORE, not
// iteration order.
func capPins(out []Candidate) []Candidate {
	byRank := func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Score > out[j].Score
	}
	sort.SliceStable(out, byRank)
	pins := 0
	demoted := false
	for i := range out {
		if !out[i].Pinned {
			continue
		}
		if pins < maxPins {
			pins++
			continue
		}
		out[i].Pinned = false
		out[i].Score += pinFallback
		demoted = true
	}
	if demoted {
		sort.SliceStable(out, byRank)
	}
	return out
}

// Baseline returns SessionStart cards for a scope: baseline-flagged cards
// first (narrowest scope first), for the packer to budget.
func (r *Retriever) Baseline(ctx context.Context, sc Scope) ([]Candidate, error) {
	metas, err := r.Store.AllCardsMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	var out []Candidate
	for _, c := range metas {
		if !c.Baseline || c.Status == "candidate" || c.Status == "retired" {
			continue
		}
		rank := c.NarrowestScopeRank(sc.Allowed)
		if rank < 0 {
			continue
		}
		out = append(out, Candidate{Card: c, Score: float64(rank)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// matchesTrigger reports whether any keyword occurs in the folded prompt with
// word boundaries. Keywords < 4 chars are ignored (too noisy to pin).
func matchesTrigger(keywords []string, foldedPrompt string) bool {
	for _, kw := range keywords {
		k := fold(kw)
		if len(k) < 4 {
			continue
		}
		idx := 0
		for {
			i := strings.Index(foldedPrompt[idx:], k)
			if i < 0 {
				break
			}
			start := idx + i
			end := start + len(k)
			if boundaryAt(foldedPrompt, start-1) && boundaryAt(foldedPrompt, end) {
				return true
			}
			idx = end
		}
	}
	return false
}

func boundaryAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	return (c < 'a' || c > 'z') && (c < '0' || c > '9')
}

// matchedTerms counts distinct query terms present in the card's searchable
// text (title + summary + aliases + trigger keywords). Matching is by shared
// 4-char prefix — a poor-man's stem that agrees with FTS5's porter stemmer on
// the common cases (retry/retries, handling/handlers) without shipping one.
func matchedTerms(c store.StoredCard, queryTerms []string) int {
	hayWords := terms(c.Title+" "+c.Summary+" "+
		strings.Join(c.Aliases, " ")+" "+strings.Join(c.Triggers.Keywords, " "), 0)
	n := 0
	for _, q := range queryTerms {
		for _, h := range hayWords {
			if stemEqual(q, h) {
				n++
				break
			}
		}
	}
	return n
}

// stemEqual: exact match, or both ≥4 chars sharing a 4-char prefix.
func stemEqual(a, b string) bool {
	if a == b {
		return true
	}
	return len(a) >= 4 && len(b) >= 4 && a[:4] == b[:4]
}
