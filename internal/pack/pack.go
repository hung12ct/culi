// Package pack turns ranked candidates into a budgeted injection envelope.
// Granularity ladder per card: body (≤400 tok) → summary → hook line. The
// budget is under-filled to ~90% so token-estimation error can never blow the
// hard cap (C3).
package pack

import (
	"strings"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

const (
	maxCards     = 8
	maxHookLines = 4
	maxBodyTok   = 400 // longer bodies are pull-only (Phase 3 expand_card)
	envOpen      = "<ctx>"
	envClose     = "</ctx>"
	fillPercent  = 90
)

// Item is one packed card at one granularity.
type Item struct {
	CardID      string
	ShortID     string
	Granularity string
	Tokens      int
	Text        string
}

// Injection is the rendered result. Empty Items ⇒ inject nothing.
type Injection struct {
	Items  []Item
	Tokens int
}

// BodyLoader hydrates bodies for the few cards packed at body granularity —
// the hot-path scan deliberately omits bodies.
type BodyLoader func(rowids []int64) (map[int64]string, error)

// Pack fills the budget greedily in rank order. injected holds the session's
// prior granularity levels (monotonic dedup: a card re-emits only at a
// strictly higher level).
func Pack(cands []retrieve.Candidate, injected map[string]int, budget int, load BodyLoader) (Injection, error) {
	cands = mmrOrder(cands) // near-duplicates cost one slot, not two
	usable := budget * fillPercent / 100
	spent := 0
	hookLines := 0
	var out Injection

	// Hydrate bodies once for every candidate that could plausibly go out at
	// body granularity (rank 0 or pinned).
	var needBodies []int64
	for i, c := range cands {
		if (i == 0 || c.Pinned) && c.Card.TokBody <= maxBodyTok {
			needBodies = append(needBodies, c.Card.Rowid)
		}
	}
	bodies := map[int64]string{}
	if len(needBodies) > 0 && load != nil {
		var err error
		bodies, err = load(needBodies)
		if err != nil {
			// Degrade, don't fail: pack summaries instead (C1 fail-open ethos).
			bodies = map[int64]string{}
		}
	}

	for i, c := range cands {
		if len(out.Items) >= maxCards {
			break
		}
		ceiling := store.GranHook
		switch {
		case (i == 0 || c.Pinned) && c.Card.TokBody <= maxBodyTok:
			ceiling = store.GranBody
		case i < 4:
			ceiling = store.GranSummary
		}

		item, ok := fit(c.Card, ceiling, injected[c.Card.ID], usable-spent, bodies)
		if !ok {
			continue
		}
		if item.Granularity == store.GranHook {
			if hookLines >= maxHookLines {
				continue
			}
			hookLines++
		}
		spent += item.Tokens
		out.Items = append(out.Items, item)
	}
	out.Tokens = spent
	return out, nil
}

// fit picks the highest affordable granularity ≤ ceiling and > alreadyLevel.
func fit(c store.StoredCard, ceiling string, alreadyLevel, remaining int, bodies map[int64]string) (Item, bool) {
	for _, gran := range []string{store.GranBody, store.GranSummary, store.GranHook} {
		if store.GranLevel(gran) > store.GranLevel(ceiling) {
			continue
		}
		if store.GranLevel(gran) <= alreadyLevel {
			return Item{}, false // monotonic dedup: only upgrades ship (14.43)
		}
		text, tokens := render(c, gran, bodies)
		if text == "" || tokens > remaining {
			continue
		}
		return Item{CardID: c.ID, ShortID: c.ShortID, Granularity: gran, Tokens: tokens, Text: text}, true
	}
	return Item{}, false
}

func render(c store.StoredCard, gran string, bodies map[int64]string) (string, int) {
	switch gran {
	case store.GranBody:
		body := bodies[c.Rowid]
		if body == "" {
			return "", 0 // body unavailable → caller falls through to summary
		}
		text := "[" + c.Type + "] " + c.Title + " — " + body
		return text, knowledge.EstimateTokens(text)
	case store.GranSummary:
		text := "[" + c.Type + "] " + c.Title + ": " + c.Summary
		return text, knowledge.EstimateTokens(text)
	default:
		text := "▸ " + c.ShortID + " " + c.Title + ": " + firstLine(c.Summary)
		return text, knowledge.EstimateTokens(text)
	}
}

// Render produces the final envelope string ("" when nothing packed).
func (inj Injection) Render() string {
	if len(inj.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(envOpen)
	b.WriteByte('\n')
	for _, it := range inj.Items {
		b.WriteString(it.Text)
		b.WriteByte('\n')
	}
	b.WriteString(envClose)
	return b.String()
}

// Records converts packed items into injection-log rows.
func (inj Injection) Records() []store.InjectionRecord {
	out := make([]store.InjectionRecord, 0, len(inj.Items))
	for _, it := range inj.Items {
		out = append(out, store.InjectionRecord{CardID: it.CardID, Granularity: it.Granularity, Tokens: it.Tokens})
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
