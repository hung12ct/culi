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
	maxCards    = 8
	maxBodyTok  = 400 // longer bodies are pull-only (Phase 3 expand_card)
	envOpen     = "<ctx>"
	envClose    = "</ctx>"
	fillPercent = 90
)

// DefaultHookLines is how many "▸ id Title: teaser" pointer lines an injection
// may carry. Zero by default, on evidence: across three weeks of daily use the
// expand_card counter moved exactly once, over 78 pointer injections costing
// 4,177 tokens (11.4% of all injected spend). A pointer only does something if
// the model pulls it, and in practice it does not.
//
// Emitting them was also the sole feed for PenalizeAbandonedPointers, so every
// pointer became a downvote at a ~100% base rate — negative feedback accrued
// 2.1x faster than positive, dragging the whole corpus toward "noisy"
// regardless of merit. Dropping the granularity fixes the token waste and the
// signal corruption together.
//
// Raise it (config `pointer_lines`) to restore the ladder's third rung: cards
// beyond the summary budget are then teased rather than dropped.
const DefaultHookLines = 0

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
// strictly higher level). hookLines caps pointer lines (see DefaultHookLines);
// at 0 a card that only fits as a pointer is dropped instead of teased.
func Pack(cands []retrieve.Candidate, injected map[string]int, budget, hookLines int, load BodyLoader) (Injection, error) {
	cands = mmrOrder(cands) // near-duplicates cost one slot, not two
	usable := budget * fillPercent / 100
	spent := 0
	hooksUsed := 0
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
			if hooksUsed >= hookLines {
				continue // pointer budget spent (0 ⇒ pointers disabled entirely)
			}
			hooksUsed++
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

// PointerHeader is the one-time SessionStart preface that teaches the model
// what ▸ pointer lines are and how to pull them (plan §6). Without it a
// fresh session sees bare pointers with no in-band instruction and tends to
// ignore them. Its token cost is charged against the baseline budget by the
// caller.
const PointerHeader = `culi context cards follow. Lines starting with ▸ are collapsed cards ("▸ <id> <title>: <gist>") — expand one with the culi MCP tool expand_card(<id>); search more with search_context.`

// Render produces the final envelope string ("" when nothing packed).
func (inj Injection) Render() string { return inj.RenderWith("") }

// RenderWith prefaces the envelope with a non-card header line (e.g.
// PointerHeader). Header text is never recorded in the injection log — it is
// instruction, not knowledge, so 14.43's dedup does not apply.
func (inj Injection) RenderWith(header string) string {
	if len(inj.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(envOpen)
	b.WriteByte('\n')
	if header != "" {
		b.WriteString(header)
		b.WriteByte('\n')
	}
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
