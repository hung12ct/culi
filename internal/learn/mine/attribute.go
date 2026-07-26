package mine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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
// those. Phrase reuse is far stricter — a shared four-word sequence of prose is
// hard to produce by coincidence.
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

// attributeSession re-reads a finished session's whole transcript and credits
// the cards it appears to have used. Reading from offset 0 (rather than the
// mining cursor) is what makes the credit idempotent per session: mining
// advances its cursor as Stop refreshes the job, so cursor-relative entries
// would only ever show the tail, and a card injected early would be invisible.
// One extra parse per session, off the hot path.
func (m *Miner) attributeSession(ctx context.Context, job queue.Job) (int, error) {
	var entries []transcript.Entry
	var err error
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
	return m.attributeUsage(ctx, job.SessionID, entries)
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
func (m *Miner) attributeUsage(ctx context.Context, sessionID string, entries []transcript.Entry) (int, error) {
	if sessionID == "" || len(entries) == 0 {
		return 0, nil
	}
	injected, err := m.Store.SessionContentCards(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("mine: attributing usage: %w", err)
	}
	if len(injected) == 0 {
		return 0, nil
	}

	userPhrases, assistantPhrases := sessionPhrases(entries)
	if len(assistantPhrases) == 0 {
		return 0, nil
	}

	credited := 0
	for _, id := range injected {
		card, err := m.Store.CardByID(ctx, id)
		if err != nil {
			continue // card retired or removed since injection
		}
		if !reused(card, userPhrases, assistantPhrases) {
			continue
		}
		if err := m.Store.AddFeedback(ctx, id, store.FeedbackReferenced, attributionCredit); err != nil {
			return credited, fmt.Errorf("mine: crediting %s: %w", id, err)
		}
		credited++
	}
	return credited, nil
}

// reused reports whether any of the card's prose phrases came back out of the
// assistant without the user having supplied them.
func reused(card store.StoredCard, userPhrases, assistantPhrases map[string]bool) bool {
	for phrase := range shingle(prose(card.Title + " " + card.Summary + " " + card.Body)) {
		if assistantPhrases[phrase] && !userPhrases[phrase] {
			return true
		}
	}
	return false
}

// sessionPhrases splits the transcript into what the user typed and what the
// assistant replied. Tool output is deliberately excluded: a card's wording
// appearing in a grep result is evidence about the repo, not about the card.
func sessionPhrases(entries []transcript.Entry) (user, assistant map[string]bool) {
	var ub, ab strings.Builder
	for _, e := range entries {
		switch e.Role {
		case "user":
			ub.WriteString(e.Text)
			ub.WriteByte('\n')
		case "assistant":
			ab.WriteString(e.Text)
			ab.WriteByte('\n')
		}
	}
	return shingle(ub.String()), shingle(ab.String())
}

// shingle returns the set of shingleN-word sequences carrying at least one
// substantial word. Words are folded letter/digit runs, so punctuation and
// formatting never affect matching.
func shingle(text string) map[string]bool {
	words := strings.FieldsFunc(retrieve.Fold(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	out := make(map[string]bool)
	for i := 0; i+shingleN <= len(words); i++ {
		gram := words[i : i+shingleN]
		if !hasSubstantial(gram) {
			continue
		}
		out[strings.Join(gram, " ")] = true
	}
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
