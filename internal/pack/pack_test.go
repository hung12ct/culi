package pack

import (
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

func cand(rowid int64, id, title, summary string, bodyTok int, pinned bool) retrieve.Candidate {
	return retrieve.Candidate{
		Pinned: pinned,
		Card: store.StoredCard{
			Rowid:   rowid,
			ShortID: knowledge.ShortID(id),
			Card: knowledge.Card{
				ID: id, Type: "rule", Title: title, Summary: summary,
				TokSummary: knowledge.EstimateTokens(summary), TokBody: bodyTok,
			},
		},
	}
}

func loader(bodies map[int64]string) BodyLoader {
	return func(rowids []int64) (map[int64]string, error) { return bodies, nil }
}

func TestPackGranularityLadder(t *testing.T) {
	cands := []retrieve.Candidate{
		cand(1, "a", "Top card", "top summary", 100, false),
		cand(2, "b", "Second", "second summary", 100, false),
		cand(3, "c", "Third", "third summary", 100, false),
		cand(4, "d", "Fourth", "fourth summary", 100, false),
		cand(5, "e", "Fifth", "fifth summary", 100, false),
	}
	inj, err := Pack(cands, nil, 700, 4, loader(map[int64]string{1: "full body of the top card"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Items) != 5 {
		t.Fatalf("items = %d", len(inj.Items))
	}
	if inj.Items[0].Granularity != store.GranBody {
		t.Errorf("rank 1 should get body: %+v", inj.Items[0])
	}
	for _, it := range inj.Items[1:4] {
		if it.Granularity != store.GranSummary {
			t.Errorf("rank 2-4 should get summary: %+v", it)
		}
	}
	if inj.Items[4].Granularity != store.GranHook {
		t.Errorf("rank 5 should get hook line: %+v", inj.Items[4])
	}
	text := inj.Render()
	if !strings.HasPrefix(text, "<ctx>\n") || !strings.HasSuffix(text, "</ctx>") {
		t.Errorf("envelope malformed: %q", text)
	}
	if !strings.Contains(text, "▸ ") {
		t.Errorf("hook line missing pointer glyph: %q", text)
	}
}

func TestPackBudgetDegrades(t *testing.T) {
	// A tiny budget forces degradation and drops.
	longSummary := strings.Repeat("long summary text ", 30) // ~120 tokens
	cands := []retrieve.Candidate{
		cand(1, "a", "Big card", longSummary, 2000, false), // body over cap anyway
		cand(2, "b", "Second big", longSummary, 100, false),
	}
	inj, err := Pack(cands, nil, 100, 4, loader(nil))
	if err != nil {
		t.Fatal(err)
	}
	// 90 usable tokens: neither long summary fits; both degrade to hook lines.
	for _, it := range inj.Items {
		if it.Granularity != store.GranHook {
			t.Errorf("expected hook-line degrade, got %+v", it)
		}
	}
	if inj.Tokens > 90 {
		t.Errorf("budget blown: %d > 90", inj.Tokens)
	}
}

func TestPackMonotonicDedup(t *testing.T) {
	cands := []retrieve.Candidate{
		cand(1, "a", "Already at body", "summary a", 50, false),
		cand(2, "b", "Already at hook", "summary b", 50, false),
	}
	injected := map[string]int{
		"a": store.GranLevel(store.GranBody), // fully injected → skip entirely
		"b": store.GranLevel(store.GranHook), // hook already → can upgrade to summary+
	}
	inj, err := Pack(cands, injected, 700, 4, loader(map[int64]string{1: "body a"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Items) != 1 || inj.Items[0].CardID != "b" {
		t.Fatalf("dedup failed: %+v", inj.Items)
	}
	if store.GranLevel(inj.Items[0].Granularity) <= store.GranLevel(store.GranHook) {
		t.Errorf("upgrade not above prior level: %+v", inj.Items[0])
	}
}

func TestPackEmpty(t *testing.T) {
	inj, err := Pack(nil, nil, 700, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inj.Render() != "" || len(inj.Records()) != 0 {
		t.Errorf("empty pack should render empty: %+v", inj)
	}
}

func TestPackCaps(t *testing.T) {
	var cands []retrieve.Candidate
	for i := int64(0); i < 20; i++ {
		id := string(rune('a' + i))
		cands = append(cands, cand(i+1, id, "Card "+id, "summary "+id, 5000, false))
	}
	inj, err := Pack(cands, nil, 5000, 4, loader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Items) > 8 {
		t.Errorf("card cap exceeded: %d", len(inj.Items))
	}
	hooks := 0
	for _, it := range inj.Items {
		if it.Granularity == store.GranHook {
			hooks++
		}
	}
	if hooks > 4 {
		t.Errorf("hook-line cap exceeded: %d", hooks)
	}
}

func TestMMRDemotesNearDuplicates(t *testing.T) {
	v1 := []float32{1, 0}
	v2 := []float32{0, 1}
	a := cand(1, "rules/a", "A", "sum a", 0, false)
	a.Score, a.Vec = 0.030, v1
	dup := cand(2, "rules/a-dup", "A dup", "sum a again", 0, false)
	dup.Score, dup.Vec = 0.029, v1
	c := cand(3, "rules/c", "C", "sum c", 0, false)
	c.Score, c.Vec = 0.020, v2
	cands := []retrieve.Candidate{a, dup, c}
	inj, err := Pack(cands, nil, 700, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Items) != 3 {
		t.Fatalf("packed %d items, want 3", len(inj.Items))
	}
	got := []string{inj.Items[0].CardID, inj.Items[1].CardID, inj.Items[2].CardID}
	want := []string{"rules/a", "rules/c", "rules/a-dup"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MMR order = %v, want %v", got, want)
		}
	}
}

func TestMMRKeepsPinsFirst(t *testing.T) {
	v1 := []float32{1, 0}
	pin := cand(1, "rules/pin", "Pinned", "sum", 0, true)
	pin.Score, pin.Vec = 0.001, v1
	top := cand(2, "rules/top", "Top", "sum", 0, false)
	top.Score, top.Vec = 0.050, v1
	cands := []retrieve.Candidate{pin, top}
	inj, err := Pack(cands, nil, 700, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Items) < 2 || inj.Items[0].CardID != "rules/pin" {
		t.Fatalf("pin lost its slot: %+v", inj.Items)
	}
}

// The default: pointer lines are off, so a card that only fits as a "▸" line
// is dropped rather than teased. Three weeks of production data showed exactly
// one expand_card call across 78 pointer injections, while those injections
// were the sole feed for the abandoned-pointer penalty.
func TestPointersDisabledDropsInsteadOfTeasing(t *testing.T) {
	cands := []retrieve.Candidate{
		cand(1, "a", "Top card", "summary a", 40, false),
		cand(2, "b", "Second", "summary b", 40, false),
		cand(3, "c", "Third", "summary c", 40, false),
		cand(4, "d", "Fourth", "summary d", 40, false),
		cand(5, "e", "Fifth — pointer only", "summary e", 40, false),
		cand(6, "f", "Sixth — pointer only", "summary f", 40, false),
	}
	off, err := Pack(cands, nil, 700, DefaultHookLines, loader(map[int64]string{1: "body a"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range off.Items {
		if it.Granularity == store.GranHook {
			t.Errorf("pointer emitted with hookLines=0: %+v", it)
		}
	}

	// The mechanism still works when re-enabled — this is a default, not a removal.
	on, err := Pack(cands, nil, 700, 4, loader(map[int64]string{1: "body a"}))
	if err != nil {
		t.Fatal(err)
	}
	hooks := 0
	for _, it := range on.Items {
		if it.Granularity == store.GranHook {
			hooks++
		}
	}
	if hooks == 0 {
		t.Fatal("expected pointer lines when hookLines=4; the ladder must remain reachable")
	}
	if len(off.Items) >= len(on.Items) {
		t.Errorf("disabling pointers should drop cards: off=%d on=%d", len(off.Items), len(on.Items))
	}
	if off.Tokens >= on.Tokens {
		t.Errorf("disabling pointers should cost fewer tokens: off=%d on=%d", off.Tokens, on.Tokens)
	}
}
