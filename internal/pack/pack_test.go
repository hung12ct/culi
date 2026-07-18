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
	inj, err := Pack(cands, nil, 700, loader(map[int64]string{1: "full body of the top card"}))
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
	inj, err := Pack(cands, nil, 100, loader(nil))
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
	inj, err := Pack(cands, injected, 700, loader(map[int64]string{1: "body a"}))
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
	inj, err := Pack(nil, nil, 700, nil)
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
	inj, err := Pack(cands, nil, 5000, loader(nil))
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
