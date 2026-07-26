package mine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/learn/codexroll"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/learn/transcript"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// Usage attribution: the only automatic positive signal culi has.
//
// FeedbackReferenced was previously written in exactly one place — the miner
// re-observing an existing card — which records that a lesson recurred, not
// that a card was ever used. FeedbackExpanded needs an MCP expand_card call, so
// a card pushed at body granularity (the bulk of injected tokens) could help on
// every prompt and still score zero forever. Every card then classified
// "uncertain" and UtilityMultipliers stayed pinned at 1.0, so ranking never
// learned anything.
//
// This closes the loop by looking for the card's own prose coming back out in
// the assistant's replies. Word-overlap was tried first and rejected: measuring
// term rarity against the card corpus marks ordinary English ("also", "keeps",
// "development") as distinctive, because card summaries are short and
// technical, and it credited over half of all injected cards on words like
// those. Phrase reuse is far stricter — a shared five-word sequence of prose is
// hard to produce by coincidence.
//
// Two known limits, both erring toward missing credit rather than inventing it:
// words are folded letter/digit runs, so a CJK session produces no phrases and
// is never attributed; and cards imported from a repo's CLAUDE.md/AGENTS.md
// share prose with text the harness already loads every turn, so a reply
// restating it credits the card even though the injection was not the source —
// a bias toward exactly the duplicated cards culi exists to collapse.
const (
	// shingleN is the phrase length, calibrated against real sessions. Four
	// words still collides on stock English — "starting a new feature" appears
	// in a skill summary and in any reply about starting a feature. Five is the
	// point where matches became the card's own guidance being restated
	// ("without the matching changelog entry") rather than filler. It credits
	// 1-2 cards per session where word-overlap credited 10+; sparse and
	// trustworthy beats frequent and wrong, since credits accumulate over many
	// sessions and one false positive marks a card "helpful" permanently.
	shingleN = 5

	// substantialWordLen: a phrase made entirely of short function words
	// ("all clean and the") is English, not a fingerprint. At least one word
	// must clear this length.
	substantialWordLen = 6

	// attributionCredit is one session's worth of "this card was probably
	// used". Below the 1.0 a confirmed re-observation earns, because phrase
	// reuse is weaker evidence than the miner independently rediscovering the
	// same lesson. Weight 5 (wReferenced) still applies on top.
	attributionCredit = 0.5
)

// attributeSession credits the cards a finished session appears to have used.
//
// Ordering is deliberate, cheapest guard first: a full transcript parse costs
// ~385ms on a 33MB session, so it must not run for sessions that cannot be
// credited (nothing injected, or already credited). The claim is what makes
// this idempotent — see store.ClaimAttribution; the job's "session-end" label
// is not once-per-session.
//
// Matching needs the whole session, not the mining cursor's tail, or a card
// injected early would be invisible. When the cursor never moved, MineSession's
// entries already are the whole session, so they are reused instead of
// re-parsed.
func (m *Miner) attributeSession(ctx context.Context, job queue.Job, cur queue.Cursor, entries []transcript.Entry) (int, error) {
	injected, err := m.Store.SessionContentCards(ctx, job.SessionID)
	if err != nil {
		return 0, fmt.Errorf("mine: attributing usage: %w", err)
	}
	if len(injected) == 0 {
		return 0, nil // nothing whose content reached the model
	}
	claimed, err := m.Store.ClaimAttribution(ctx, job.SessionID, time.Now())
	if err != nil {
		return 0, err
	}
	if !claimed {
		return 0, nil // already credited by an earlier final job
	}

	if cur.Offset != 0 {
		switch job.EffectiveSource() {
		case harness.Claude:
			entries, _, err = transcript.ReadFrom(job.TranscriptPath, 0)
		case harness.Codex:
			entries, _, err = codexroll.ReadFrom(job.TranscriptPath, 0)
		default:
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("mine: re-reading transcript for attribution: %w", err)
		}
	}
	return m.attributeUsage(ctx, injected, entries)
}

// attributeUsage credits cards whose prose reappears in the assistant's
// replies this session. Returns the number of cards credited.
//
// Guards, in order of importance:
//   - Only cards injected at summary/body granularity (SessionContentCards): a
//     pointer the model never expanded cannot have been used, and pointers
//     already have the opposite contract in PenalizeAbandonedPointers.
//   - Phrases the user typed are excluded. If you paste a card's wording into
//     your prompt, the reply echoing it proves nothing about the injection.
//   - Code, URLs and filesystem paths are stripped from card bodies before
//     matching. A card and a reply both naming ~/.culi/knowledge share a
//     "phrase" that is about the repo, not about the card.
//
// Best-effort: attribution is a nudge to ranking, never a reason to fail a run.
func (m *Miner) attributeUsage(ctx context.Context, injected []string, entries []transcript.Entry) (int, error) {
	if len(injected) == 0 || len(entries) == 0 {
		return 0, nil
	}

	index := make(map[string][]string, len(injected)*64)
	for _, id := range injected {
		card, err := m.Store.CardByID(ctx, id)
		if err != nil {
			continue // retired or removed since injection
		}
		for phrase := range shingle(prose(card.Title + " " + card.Summary + " " + card.Body)) {
			index[phrase] = append(index[phrase], id)
		}
	}
	if len(index) == 0 {
		return 0, nil
	}

	// Match by streaming the transcript against the card index rather than
	// shingling the transcript: sessions reach 30MB / 350k words, whose phrase
	// set would cost tens of MB, while the index is bounded by the handful of
	// cards injected. Phrases are built per entry, so a "phrase" can never span
	// two messages.
	userSupplied := make(map[string]bool)
	for _, e := range entries {
		if e.Role != "user" {
			continue
		}
		eachShingle(e.Text, func(phrase string) {
			if _, ok := index[phrase]; ok {
				userSupplied[phrase] = true
			}
		})
	}
	hit := make(map[string]bool, len(injected))
	for _, e := range entries {
		if e.Role != "assistant" {
			continue
		}
		eachShingle(e.Text, func(phrase string) {
			if userSupplied[phrase] {
				return
			}
			for _, id := range index[phrase] {
				hit[id] = true
			}
		})
	}

	credited := 0
	for _, id := range injected {
		if !hit[id] {
			continue
		}
		if err := m.Store.AddFeedback(ctx, id, store.FeedbackReferenced, attributionCredit); err != nil {
			return credited, fmt.Errorf("mine: crediting %s: %w", id, err)
		}
		credited++
	}
	return credited, nil
}

// eachShingle calls fn for every shingleN-word sequence in text that carries at
// least one substantial word. Words are folded letter/digit runs, so
// punctuation and formatting never affect matching.
func eachShingle(text string, fn func(string)) {
	words := strings.FieldsFunc(retrieve.Fold(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for i := 0; i+shingleN <= len(words); i++ {
		gram := words[i : i+shingleN]
		if !hasSubstantial(gram) {
			continue
		}
		fn(strings.Join(gram, " "))
	}
}

// shingle collects eachShingle into a set — used for the small card side,
// never for a transcript.
func shingle(text string) map[string]bool {
	out := make(map[string]bool)
	eachShingle(text, func(p string) { out[p] = true })
	return out
}

func hasSubstantial(gram []string) bool {
	for _, w := range gram {
		if len(w) >= substantialWordLen {
			return true
		}
	}
	return false
}

// stripPatterns remove the coincidence factory: two texts both quoting the
// same path, URL or code identifier share a phrase that says nothing about
// whether one influenced the other.
var stripPatterns = []*regexp.Regexp{
	regexp.MustCompile("`[^`]*`"),                // inline code spans
	regexp.MustCompile(`https?://\S+`),           // URLs
	regexp.MustCompile(`~?/[\w./@-]+`),           // absolute / home-relative paths
	regexp.MustCompile(`\b[\w-]+\.[a-z]{2,4}\b`), // file.ext, host.com
}

// prose strips fenced code blocks and the patterns above from markdown.
func prose(md string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := b.String()
	for _, re := range stripPatterns {
		out = re.ReplaceAllString(out, " ")
	}
	return out
}
