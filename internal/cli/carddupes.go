package cli

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/hung12ct/culi/internal/carddedup"
	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/mine"
	"github.com/hung12ct/culi/internal/store"
)

// dupePair is two live cards that say the same thing, already oriented:
// keep is the one with the better track record.
type dupePair struct {
	keep, drop store.StoredCard
	sim        float64
	arm        string // "cosine" | "jaccard"
}

// cardDupes reports live cards that duplicate each other and can retire the
// weaker twin of each pair.
//
// Twins are not cosmetic: each copy collects its own share of the injection
// and feedback signal, so two halves of one rule can both sit at "uncertain"
// forever while the merged card would have earned a verdict. Retiring is a
// reversible status flip on a git-tracked file (C4), never a delete.
func cardDupes(ctx context.Context, s *store.Store, base string, args []string) error {
	fs := flag.NewFlagSet("card dupes", flag.ContinueOnError)
	retire := fs.Bool("retire", false, "retire the weaker twin of each pair (reversible; commits to knowledge/)")
	near := fs.Bool("near", false, "also list near-duplicates (never retired; review them by hand)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	_, cfg, err := loadBase()
	if err != nil {
		return err
	}
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return err
	}
	var live []store.StoredCard
	for _, c := range cards {
		if c.Status != "retired" && c.Status != "candidate" {
			live = append(live, c)
		}
	}
	vecs, err := s.Embeddings(ctx, cfg.Ollama.Model)
	if err != nil || len(vecs) == 0 {
		fmt.Println("note:   no embeddings — lexical comparison only; `culi index` improves this")
	}
	rank, err := cardRanking(ctx, s)
	if err != nil {
		return err
	}

	dups, nears := findDupePairs(live, vecs, rank)
	if *near {
		for _, p := range nears {
			fmt.Printf("near     %.3f %-8s %-44s ~ %s\n", p.sim, p.arm, p.keep.ID, p.drop.ID)
		}
		if len(nears) > 0 {
			fmt.Println()
		}
	}
	if len(dups) == 0 {
		fmt.Println("no duplicate pairs")
		return nil
	}
	for _, p := range dups {
		fmt.Printf("dup      %.3f %-8s keep %-44s drop %s\n", p.sim, p.arm, p.keep.ID, p.drop.ID)
	}
	if !*retire {
		fmt.Printf("\n%d pair(s). Re-run with --retire to retire the dropped side (reversible).\n", len(dups))
		return nil
	}

	kdir := config.KnowledgeDir(base)
	retired := 0
	for _, p := range dups {
		ok, err := mine.RetireCard(ctx, s, kdir, p.drop.ID)
		if err != nil {
			return fmt.Errorf("cli: retiring %s: %w", p.drop.ID, err)
		}
		if !ok {
			fmt.Printf("skipped  %s — hand-authored; edit its status field to retire\n", p.drop.ID)
			continue
		}
		fmt.Printf("retired  %s\n", p.drop.ID)
		retired++
	}
	if retired == 0 {
		return nil
	}
	if err := knowledge.Commit(kdir, fmt.Sprintf("card: retired %d duplicate card(s)", retired)); err != nil {
		fmt.Printf("note:    retired on disk but not committed: %v\n", err)
	}
	fmt.Printf("\nretired %d card(s) — run `culi index` to drop them from retrieval\n", retired)
	return nil
}

// cardRanking scores each card by how well it has earned its place: window
// injections first (proven exposure), then positive feedback. Used only to
// orient a pair, never to decide that one exists.
func cardRanking(ctx context.Context, s *store.Store) (map[string]float64, error) {
	usage, err := s.CardWindowUsage(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.AllCardStats(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	rank := make(map[string]float64, len(usage)+len(stats))
	for id, u := range usage {
		rank[id] += float64(u.Injections)
	}
	for id, cs := range stats {
		rank[id] += cs.Expanded + cs.Referenced - cs.Downvoted
	}
	return rank, nil
}

// findDupePairs groups live cards into duplicate clusters and returns one
// pair per card that should go, each aimed at the cluster's keeper.
//
// Clustering rather than greedy pairing, because duplicates arrive in threes:
// three phrasings of one rule produce three Dup edges, and matching them
// pairwise can name a card the keeper of one pair and the drop of another —
// retire both sides and the rule loses every live copy. A cluster has exactly
// one keeper by construction.
func findDupePairs(live []store.StoredCard, vecs map[int64][]float32, rank map[string]float64) (dups, nears []dupePair) {
	parent := make([]int, len(live))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	// bestEdge[i] is the strongest similarity linking card i into its cluster —
	// what the report shows for the card, since it may never have been compared
	// directly against the keeper that ends up surviving.
	bestEdge := make([]dupePair, len(live))

	for i := 0; i < len(live); i++ {
		for j := i + 1; j < len(live); j++ {
			a, b := live[i], live[j]
			if a.Type != b.Type {
				continue
			}
			sim, arm, verdict := compare(a, b, vecs)
			if verdict == carddedup.Dup && !sameScope(a, b) {
				// Same words, different reach: a global rule and its
				// repo-scoped twin are not interchangeable, and retiring the
				// broader one would silently drop coverage in every other
				// repo. Demote to the review band and let a human decide.
				verdict = carddedup.Near
			}
			switch verdict {
			case carddedup.New:
				continue
			case carddedup.Near:
				keep, drop := orient(a, b, rank)
				nears = append(nears, dupePair{keep: keep, drop: drop, sim: sim, arm: arm})
				continue
			}
			parent[find(i)] = find(j)
			for _, k := range [2]int{i, j} {
				if sim > bestEdge[k].sim {
					bestEdge[k] = dupePair{sim: sim, arm: arm}
				}
			}
		}
	}

	// One keeper per cluster: the best-earning member, orient() deciding ties.
	keeper := map[int]int{}
	for i := range live {
		root := find(i)
		cur, ok := keeper[root]
		if !ok {
			keeper[root] = i
			continue
		}
		if k, _ := orient(live[cur], live[i], rank); k.ID == live[i].ID {
			keeper[root] = i
		}
	}
	for i := range live {
		root := find(i)
		k := keeper[root]
		if k == i {
			continue
		}
		dups = append(dups, dupePair{keep: live[k], drop: live[i], sim: bestEdge[i].sim, arm: bestEdge[i].arm})
	}

	// Deterministic output: similarity first, then ID, so a dry run and the
	// --retire run that follows it print the same list in the same order.
	byRank := func(p []dupePair) func(int, int) bool {
		return func(i, j int) bool {
			if p[i].sim != p[j].sim {
				return p[i].sim > p[j].sim
			}
			return p[i].drop.ID < p[j].drop.ID
		}
	}
	sort.Slice(dups, byRank(dups))
	sort.Slice(nears, byRank(nears))
	return dups, nears
}

// compare scores one pair on whichever arm is available, taking the closer
// verdict when both are — the same asymmetry the importer uses, for the same
// reason: a rephrasing shares meaning without sharing many terms.
func compare(a, b store.StoredCard, vecs map[int64][]float32) (float64, string, carddedup.Verdict) {
	_, jac := carddedup.BestLexical(a.Title, a.Summary, []store.StoredCard{b})
	sim, arm, verdict := jac, "jaccard", carddedup.ClassifyLexical(jac)
	va, okA := vecs[a.Rowid]
	vb, okB := vecs[b.Rowid]
	if okA && okB {
		if _, cos, ok := carddedup.BestVector(va, []store.StoredCard{b}, map[int64][]float32{b.Rowid: vb}); ok {
			if v := carddedup.Classify(cos); v > verdict {
				sim, arm, verdict = cos, "cosine", v
			}
		}
	}
	return sim, arm, verdict
}

// orient puts the better-earning card on the keep side. Ties break on ID so
// a dry run and the --retire run that follows it always agree.
func orient(a, b store.StoredCard, rank map[string]float64) (keep, drop store.StoredCard) {
	ra, rb := rank[a.ID], rank[b.ID]
	switch {
	case ra > rb:
		return a, b
	case rb > ra:
		return b, a
	case len(a.ID) != len(b.ID): // the awkward re-slug is usually the longer one
		if len(a.ID) < len(b.ID) {
			return a, b
		}
		return b, a
	case a.ID < b.ID:
		return a, b
	}
	return b, a
}

// sameScope reports whether two cards reach exactly the same set of contexts.
func sameScope(a, b store.StoredCard) bool {
	if len(a.Scopes) != len(b.Scopes) {
		return false
	}
	x, y := slices.Clone(a.Scopes), slices.Clone(b.Scopes)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
