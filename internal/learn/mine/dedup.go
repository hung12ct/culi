package mine

import (
	"context"
	"time"

	"github.com/hung12ct/culi/internal/carddedup"
	"github.com/hung12ct/culi/internal/store"
)

// embedCap bounds the one embedding call findMatch makes per candidate.
const embedCap = 2 * time.Second

// findMatch compares one candidate against existing same-type cards
// (retired excluded — a retired lesson must not swallow its successor).
// Embedding similarity when vectors are available, lexical Jaccard otherwise.
// Thresholds live in carddedup so the importer agrees with us on what a
// duplicate is.
func (m *Miner) findMatch(ctx context.Context, c cardOut, typ string) (store.StoredCard, carddedup.Verdict) {
	cards, err := m.Store.AllCardsMeta(ctx)
	if err != nil {
		return store.StoredCard{}, carddedup.New
	}
	existing := carddedup.Eligible(cards, typ)
	if len(existing) == 0 {
		return store.StoredCard{}, carddedup.New
	}
	if best, verdict, ok := m.embedMatch(ctx, c, existing); ok {
		return best, verdict
	}
	best, sim := carddedup.BestLexical(c.Title, c.Summary, existing)
	return best, carddedup.ClassifyLexical(sim)
}

// embedMatch is the primary path: cosine of the candidate's title+summary
// against stored card vectors. ok=false (dead Ollama, no vectors) falls back
// to lexical.
func (m *Miner) embedMatch(ctx context.Context, c cardOut, existing []store.StoredCard) (store.StoredCard, carddedup.Verdict, bool) {
	if m.Emb == nil {
		return store.StoredCard{}, carddedup.New, false
	}
	if m.vecs == nil {
		vecs, err := m.Store.Embeddings(ctx, m.EmbModel)
		if err != nil || len(vecs) == 0 {
			return store.StoredCard{}, carddedup.New, false
		}
		m.vecs = vecs
	}
	ectx, cancel := context.WithTimeout(ctx, embedCap)
	defer cancel()
	qv, err := m.Emb.Embed(ectx, []string{c.Title + "\n" + c.Summary})
	if err != nil || len(qv) != 1 {
		return store.StoredCard{}, carddedup.New, false
	}
	best, sim, ok := carddedup.BestVector(qv[0], existing, m.vecs)
	if !ok {
		return store.StoredCard{}, carddedup.New, false // no vectors for this type yet
	}
	return best, carddedup.Classify(sim), true
}
