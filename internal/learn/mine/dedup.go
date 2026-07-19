package mine

import (
	"context"
	"time"

	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// Dedup thresholds (plan §learning A). The embed pair applies to cosine
// similarity of title+summary vectors; the lexical pair is the Jaccard
// fallback when Ollama is absent. Between dup and near lies "new".
const (
	cosDup   = 0.92
	cosNear  = 0.78
	jacDup   = 0.60
	jacNear  = 0.40
	embedCap = 2 * time.Second
)

type matchVerdict int

const (
	matchNew  matchVerdict = iota // no existing card is close: create
	matchNear                     // suspiciously close: skip conservatively
	matchDup                      // same insight: reinforce the existing card
)

// findMatch compares one candidate against existing same-type cards
// (retired excluded — a retired lesson must not swallow its successor).
// Embedding similarity when vectors are available, lexical Jaccard otherwise.
func (m *Miner) findMatch(ctx context.Context, c cardOut, typ string) (store.StoredCard, matchVerdict) {
	cards, err := m.Store.AllCardsMeta(ctx)
	if err != nil {
		return store.StoredCard{}, matchNew
	}
	var existing []store.StoredCard
	for _, sc := range cards {
		if sc.Type == typ && sc.Status != "retired" {
			existing = append(existing, sc)
		}
	}
	if len(existing) == 0 {
		return store.StoredCard{}, matchNew
	}

	if best, verdict, ok := m.embedMatch(ctx, c, existing); ok {
		return best, verdict
	}
	return lexicalMatch(c, existing)
}

// embedMatch is the primary path: cosine of the candidate's title+summary
// against stored card vectors. ok=false (dead Ollama, no vectors) falls back
// to lexical.
func (m *Miner) embedMatch(ctx context.Context, c cardOut, existing []store.StoredCard) (store.StoredCard, matchVerdict, bool) {
	if m.Emb == nil {
		return store.StoredCard{}, matchNew, false
	}
	if m.vecs == nil {
		vecs, err := m.Store.Embeddings(ctx, m.EmbModel)
		if err != nil || len(vecs) == 0 {
			return store.StoredCard{}, matchNew, false
		}
		m.vecs = vecs
	}
	ectx, cancel := context.WithTimeout(ctx, embedCap)
	defer cancel()
	qv, err := m.Emb.Embed(ectx, []string{c.Title + "\n" + c.Summary})
	if err != nil || len(qv) != 1 {
		return store.StoredCard{}, matchNew, false
	}

	var best store.StoredCard
	bestSim := -1.0
	matched := false
	for _, sc := range existing {
		v, ok := m.vecs[sc.Rowid]
		if !ok {
			continue
		}
		matched = true
		if sim := embed.Dot(qv[0], v); sim > bestSim {
			bestSim, best = sim, sc
		}
	}
	if !matched {
		return store.StoredCard{}, matchNew, false // no vectors for this type yet
	}
	switch {
	case bestSim >= cosDup:
		return best, matchDup, true
	case bestSim >= cosNear:
		return best, matchNear, true
	}
	return store.StoredCard{}, matchNew, true
}

// lexicalMatch is the BM25-era fallback: term-set Jaccard over title+summary.
func lexicalMatch(c cardOut, existing []store.StoredCard) (store.StoredCard, matchVerdict) {
	candTerms := retrieve.Terms(c.Title+" "+c.Summary, 0)
	var best store.StoredCard
	bestSim := -1.0
	for _, sc := range existing {
		sim := retrieve.Jaccard(candTerms, retrieve.Terms(sc.Title+" "+sc.Summary, 0))
		if sim > bestSim {
			bestSim, best = sim, sc
		}
	}
	switch {
	case bestSim >= jacDup:
		return best, matchDup
	case bestSim >= jacNear:
		return best, matchNear
	}
	return store.StoredCard{}, matchNew
}
