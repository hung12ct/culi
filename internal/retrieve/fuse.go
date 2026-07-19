package retrieve

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/store"
)

// Embedding-arm tuning. The arm is strictly additive: every failure mode
// (nil embedder, open breaker, timeout, stale vectors) collapses the RRF sum
// to its BM25 term — the same code path, no fallback branch.
const (
	embedTimeout = 100 * time.Millisecond
	cosDepth     = 50   // cosine arm depth, mirrors bm25Depth
	cosFloor     = 0.60 // score-floor arm: semantic match strong enough on its own
	maxEmbedText = 1200 // chars of query fed to the embedder
)

// Circuit breaker: 3 consecutive embed failures open it for 30s. State lives
// in the meta table because every hook invocation is a fresh process — an
// in-memory breaker would never trip.
const (
	breakerThreshold = 3
	breakerCooldown  = 30 * time.Second
	metaFailCount    = "embed_fail_count"
	metaBreakUntil   = "embed_break_until"
)

// arm is the embedding arm's result: the query vector plus fresh card
// vectors. Either may be nil — the fuse handles all combinations.
type arm struct {
	queryVec []float32
	vecs     map[int64][]float32
}

// startEmbedArm embeds the query (bounded by embedTimeout) and loads card
// vectors, concurrently with the BM25 arm. Always delivers exactly one value.
func (r *Retriever) startEmbedArm(ctx context.Context, query string) <-chan arm {
	ch := make(chan arm, 1)
	if r.Embedder == nil {
		ch <- arm{}
		return ch
	}
	go func() {
		if breakerOpen(ctx, r.Store, time.Now()) {
			ch <- arm{}
			return
		}
		ectx, cancel := context.WithTimeout(ctx, embedTimeout)
		defer cancel()
		if len(query) > maxEmbedText {
			query = query[:maxEmbedText]
		}
		qv, err := r.Embedder.Embed(ectx, []string{query})
		breakerRecord(ctx, r.Store, err == nil, time.Now())
		if err != nil || len(qv) != 1 {
			ch <- arm{}
			return
		}
		vecs, err := r.Store.Embeddings(ctx, r.Model)
		if err != nil {
			vecs = nil // BM25 still stands; per-card degrade
		}
		ch <- arm{queryVec: qv[0], vecs: vecs}
	}()
	return ch
}

// cosineRanks scores every in-scope card against the query vector and returns
// (rowid → 1-based rank) for the top cosDepth plus (rowid → similarity) for
// the floor check and MMR.
func cosineRanks(a arm, inScope []store.StoredCard) (map[int64]int, map[int64]float64) {
	if a.queryVec == nil || len(a.vecs) == 0 {
		return nil, nil
	}
	type hit struct {
		rowid int64
		sim   float64
	}
	hits := make([]hit, 0, len(inScope))
	sims := make(map[int64]float64, len(inScope))
	for _, c := range inScope {
		v, ok := a.vecs[c.Rowid]
		if !ok {
			continue
		}
		sim := embed.Dot(a.queryVec, v) // both normalized ⇒ cosine
		sims[c.Rowid] = sim
		hits = append(hits, hit{c.Rowid, sim})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].sim > hits[j].sim })
	if len(hits) > cosDepth {
		hits = hits[:cosDepth]
	}
	ranks := make(map[int64]int, len(hits))
	for i, h := range hits {
		ranks[h.rowid] = i + 1
	}
	return ranks, sims
}

// breakerOpen reports whether the embed circuit is open at now. Any meta
// error reads as "closed" — the worst case is one extra (fast-failing) call.
func breakerOpen(ctx context.Context, s *store.Store, now time.Time) bool {
	until, err := s.MetaGet(ctx, metaBreakUntil)
	if err != nil || until == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, until)
	return err == nil && now.Before(t)
}

// breakerRecord updates the failure streak. Writes only on state change so a
// healthy Ollama costs zero meta writes per prompt.
func breakerRecord(ctx context.Context, s *store.Store, ok bool, now time.Time) {
	raw, err := s.MetaGet(ctx, metaFailCount)
	if err != nil {
		return
	}
	count, _ := strconv.Atoi(raw)
	if ok {
		if count != 0 {
			_ = s.MetaSet(ctx, metaFailCount, "0")
		}
		return
	}
	count++
	if count >= breakerThreshold {
		_ = s.MetaSet(ctx, metaBreakUntil,
			now.Add(breakerCooldown).UTC().Format(time.RFC3339Nano))
		count = 0
	}
	_ = s.MetaSet(ctx, metaFailCount, strconv.Itoa(count))
}
