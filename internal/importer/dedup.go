package importer

import (
	"context"
	"fmt"
	"time"

	"github.com/hung12ct/culi/internal/carddedup"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// embedCap bounds the one embedding call each candidate makes.
const embedCap = 2 * time.Second

// Deduper reports whether a card about to be staged already exists in the
// knowledge store. Consumer-side seam like Merger: the store-backed
// implementation is below, tests use a fake, and a nil Deduper disables the
// check (every candidate stages, the pre-v0.7 behaviour).
type Deduper interface {
	// Duplicate returns the closest live card's ID and how close it is.
	// It is stateful: a candidate that comes back New or Near is remembered
	// and compared against for the rest of the run, so two repos' CLAUDE.md
	// files cannot stage twins of each other in a single merge.
	Duplicate(ctx context.Context, typ, title, summary string) (string, carddedup.Verdict, error)
}

// StoreDeduper compares candidates against a snapshot of the card store.
//
// Deliberately stricter than the miner's findMatch, which takes the embedding
// verdict when vectors exist and the lexical one otherwise. Here both arms
// run and the closer verdict wins, because cards staged earlier in this same
// run have no vectors yet and would be invisible to a vector-only pass — the
// within-batch twin is exactly one of the cases this exists to catch.
type StoreDeduper struct {
	cards []store.StoredCard
	vecs  map[int64][]float32
	emb   embed.Embedder
	seen  []store.StoredCard // cleared this run; lexical-only, no vectors
}

// NewStoreDeduper snapshots the corpus once. emb may be nil (lexical only),
// and a corpus with no vectors degrades the same way.
func NewStoreDeduper(ctx context.Context, s *store.Store, emb embed.Embedder, embModel string) (*StoreDeduper, error) {
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("importer: reading card corpus for dedup: %w", err)
	}
	d := &StoreDeduper{cards: cards, emb: emb}
	if emb != nil {
		if vecs, err := s.Embeddings(ctx, embModel); err == nil {
			d.vecs = vecs
		}
	}
	return d, nil
}

// Duplicate implements Deduper.
func (d *StoreDeduper) Duplicate(ctx context.Context, typ, title, summary string) (string, carddedup.Verdict, error) {
	existing := carddedup.Eligible(d.cards, typ)
	existing = append(existing, carddedup.Eligible(d.seen, typ)...)
	if len(existing) == 0 {
		d.remember(typ, title, summary)
		return "", carddedup.New, nil
	}

	bestID, best := "", carddedup.New
	take := func(sc store.StoredCard, v carddedup.Verdict) {
		if v > best {
			bestID, best = sc.ID, v
		}
	}
	if sc, sim := carddedup.BestLexical(title, summary, existing); sim >= 0 {
		take(sc, carddedup.ClassifyLexical(sim))
	}
	if d.emb != nil && len(d.vecs) > 0 {
		ectx, cancel := context.WithTimeout(ctx, embedCap)
		defer cancel()
		// A dead Ollama is not a reason to abort an import that has already
		// paid for its LLM merge: fall through on the lexical verdict alone.
		if qv, err := d.emb.Embed(ectx, []string{title + "\n" + summary}); err == nil && len(qv) == 1 {
			if sc, sim, ok := carddedup.BestVector(qv[0], existing, d.vecs); ok {
				take(sc, carddedup.Classify(sim))
			}
		}
	}
	if best != carddedup.Dup {
		d.remember(typ, title, summary)
	}
	return bestID, best, nil
}

// StagedPrefix marks a match against a card staged earlier in this same run
// rather than one already live in knowledge/. Callers render it for the user.
const StagedPrefix = "staged this run: "

// remember adds a cleared candidate to the run-local corpus. Rowid stays zero
// so BestVector skips it — intended, it has no vector until the next index.
func (d *StoreDeduper) remember(typ, title, summary string) {
	d.seen = append(d.seen, store.StoredCard{
		Card: knowledge.Card{
			Type:    typ,
			Title:   title,
			Summary: summary,
			ID:      StagedPrefix + title,
		},
	})
}

// dedupSkip reports whether a candidate card should not be staged, plus a note
// for the user's review pass.
//
// Only an outright Dup is dropped. A Near match still stages, with a note —
// the importer's input is guidance the user wrote by hand, and silently
// discarding it because it resembles an existing card would lose content the
// user cannot get back (C4). The miner makes the opposite call on the same
// band because its input is an inferred lesson that re-mines for free.
//
// A nil Deduper or a failed check stages: dedup is a tidiness pass, never a
// reason to drop imported guidance on the floor.
func dedupSkip(ctx context.Context, d Deduper, typ, title, summary string) (bool, string) {
	if d == nil {
		return false, ""
	}
	id, verdict, err := d.Duplicate(ctx, typ, title, summary)
	switch {
	case err != nil:
		return false, fmt.Sprintf("dedup check failed for %q (%v) — staged unchecked", title, err)
	case verdict == carddedup.Dup:
		return true, fmt.Sprintf("%q duplicates %s — not staged", title, id)
	case verdict == carddedup.Near:
		return false, fmt.Sprintf("%q resembles %s — staged anyway; retire one at review", title, id)
	}
	return false, ""
}
