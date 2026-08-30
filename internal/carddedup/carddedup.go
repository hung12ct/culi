// Package carddedup decides whether a candidate card says the same thing as a
// card already in the store.
//
// Two paths create cards — the transcript miner (internal/learn/mine) and the
// repo importer (internal/importer) — and they must agree on what "duplicate"
// means. They did not: the miner compared every candidate against the store,
// the importer only against the batch it was staging, so a second import run
// over the same repos re-slugged rules it had already imported and staged them
// as new cards. Two live copies then split the retrieval and feedback signal
// between them, and neither accumulated enough to leave "uncertain".
//
// Off the hot path (C2 does not apply): the embedding arm may call Ollama.
package carddedup

import (
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// Similarity thresholds (plan §learning A). The cosine pair applies to
// title+summary vectors; the Jaccard pair is the lexical fallback for when no
// embedder is configured or the corpus has no vectors yet. Between Dup and
// Near lies New.
//
// The lexical fallback is deliberately the weaker arm and is known to miss:
// on the corpus that motivated this package it caught 13 of 17 duplicate
// pairs, and the four it missed (0.48-0.55 Jaccard, 0.93-0.96 cosine) were
// rephrasings that share meaning but few terms — "uv-only python tooling" vs
// "uv tool wrapper required". Prefer the vector arm whenever vectors exist.
const (
	CosDup  = 0.92
	CosNear = 0.78
	JacDup  = 0.60
	JacNear = 0.40
)

// Verdict is how close a candidate is to the nearest existing card.
type Verdict int

const (
	New  Verdict = iota // no existing card is close: create
	Near                // suspiciously close: callers skip conservatively
	Dup                 // same insight: reinforce the existing card
)

func (v Verdict) String() string {
	switch v {
	case Dup:
		return "duplicate"
	case Near:
		return "near-duplicate"
	default:
		return "new"
	}
}

// Classify buckets a cosine similarity between title+summary vectors.
func Classify(cos float64) Verdict {
	switch {
	case cos >= CosDup:
		return Dup
	case cos >= CosNear:
		return Near
	}
	return New
}

// ClassifyLexical buckets a term-set Jaccard similarity.
func ClassifyLexical(jac float64) Verdict {
	switch {
	case jac >= JacDup:
		return Dup
	case jac >= JacNear:
		return Near
	}
	return New
}

// BestVector returns the existing card closest to qv by cosine, with its
// similarity. ok is false when no candidate has a vector, which means the
// caller must fall back to BestLexical rather than read New into the silence.
func BestVector(qv []float32, existing []store.StoredCard, vecs map[int64][]float32) (best store.StoredCard, sim float64, ok bool) {
	sim = -1
	for _, sc := range existing {
		v, has := vecs[sc.Rowid]
		if !has {
			continue
		}
		ok = true
		if d := embed.Dot(qv, v); d > sim {
			sim, best = d, sc
		}
	}
	return best, sim, ok
}

// BestLexical returns the existing card whose title+summary shares the most
// terms with the candidate's, by Jaccard.
func BestLexical(title, summary string, existing []store.StoredCard) (best store.StoredCard, sim float64) {
	cand := retrieve.Terms(title+" "+summary, 0)
	sim = -1
	for _, sc := range existing {
		if s := retrieve.Jaccard(cand, retrieve.Terms(sc.Title+" "+sc.Summary, 0)); s > sim {
			sim, best = s, sc
		}
	}
	return best, sim
}

// Eligible filters a corpus snapshot to the cards a candidate of the given
// type may be compared against: same type and not retired, since a retired
// card must not swallow its successor. Cards still awaiting review are kept —
// a second sighting of a queued candidate should reinforce it, not queue a
// twin behind it.
func Eligible(cards []store.StoredCard, typ string) []store.StoredCard {
	out := make([]store.StoredCard, 0, len(cards))
	for _, sc := range cards {
		if sc.Type == typ && sc.Status != "retired" {
			out = append(out, sc)
		}
	}
	return out
}
