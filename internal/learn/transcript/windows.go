package transcript

import (
	"fmt"
	"strings"

	"github.com/hung12ct/culi/internal/retrieve"
)

// Stage-0 tuning. Windows are the cost lever: zero windows ⇒ zero LLM calls,
// and the render cap bounds the miner's prompt regardless of session size.
const (
	winBefore       = 2
	winAfter        = 2
	maxWindows      = 12
	perEntryChars   = 600
	perToolErrChars = 300
	minRepeatTerms  = 5   // shorter prompts repeat innocently ("run the tests")
	repeatGap       = 3   // entries between copies before it counts as repeated
	repeatSim       = 0.6 // jaccard threshold for "same instruction again"
)

// Window is one signal region: entries [Start, End] around a trigger.
type Window struct {
	Reason string // correction | rejection | repeated | tool_error
	Start  int
	End    int
}

// correctionPhrases match against normalized user text (folded, apostrophes
// dropped, punctuation collapsed to spaces). en + vi packs, like the gate's
// ack lexicon.
var correctionPhrases = []string{
	// en
	"dont", "do not", "not what", "wrong", "instead", "actually",
	"i already", "i said", "i told you", "undo", "revert", "stop",
	// vi (folded)
	"khong phai", "khong dung", "sai roi", "dung lai", "da bao", "da noi", "lam lai",
}

// correctionStarts only count at the beginning of a message — "no" mid-text is
// ordinary prose, "No, ..." as an opener is a correction.
var correctionStarts = []string{"no", "khong", "sai", "wait"}

// Extract runs the deterministic trigger scan and returns merged windows,
// capped at maxWindows with tool_error windows dropped first (corrections and
// repeats carry more signal per token).
func Extract(entries []Entry) []Window {
	var wins []Window
	var userTerms [][]string // parallel to entries; nil for non-user/short
	for i, e := range entries {
		var t []string
		if e.Role == "user" && e.Text != "" {
			t = retrieve.Terms(e.Text, 0)
		}
		userTerms = append(userTerms, t)

		switch {
		case e.Role == "user" && isCorrection(e.Text):
			wins = append(wins, window("correction", i, len(entries)))
		case isRejection(e):
			wins = append(wins, window("rejection", i, len(entries)))
		case e.Role == "user" && isRepeat(userTerms, i):
			wins = append(wins, window("repeated", i, len(entries)))
		case len(e.ToolErrors) > 0:
			wins = append(wins, window("tool_error", i, len(entries)))
		}
	}
	wins = mergeOverlaps(wins)
	return capWindows(wins)
}

func window(reason string, i, n int) Window {
	return Window{Reason: reason, Start: max(0, i-winBefore), End: min(n-1, i+winAfter)}
}

// isCorrection matches the correction lexicon against normalized text.
func isCorrection(text string) bool {
	norm := normalize(text)
	if norm == "" {
		return false
	}
	for _, p := range correctionStarts {
		if norm == p || strings.HasPrefix(norm, p+" ") {
			return true
		}
	}
	padded := " " + norm + " "
	for _, p := range correctionPhrases {
		if strings.Contains(padded, " "+p+" ") {
			return true
		}
	}
	return false
}

// isRejection detects the interrupted/rejected-tool marker Claude Code writes
// as an is_error tool_result — the strongest correction signal there is.
func isRejection(e Entry) bool {
	for _, te := range e.ToolErrors {
		f := retrieve.Fold(te)
		if strings.Contains(f, "doesn't want to proceed") ||
			strings.Contains(f, "user said") ||
			strings.Contains(f, "interrupted by user") {
			return true
		}
	}
	return false
}

// isRepeat reports whether entry i's terms near-duplicate an earlier,
// non-adjacent user message — the "I keep telling it the same thing" signal
// for a missing rule.
func isRepeat(userTerms [][]string, i int) bool {
	if len(userTerms[i]) < minRepeatTerms {
		return false
	}
	for j := 0; j < i-repeatGap; j++ {
		if len(userTerms[j]) < minRepeatTerms {
			continue
		}
		if retrieve.Jaccard(userTerms[i], userTerms[j]) >= repeatSim {
			return true
		}
	}
	return false
}

// mergeOverlaps folds overlapping/adjacent windows; the higher-priority reason
// labels the merged window.
func mergeOverlaps(wins []Window) []Window {
	if len(wins) < 2 {
		return wins
	}
	out := wins[:1]
	for _, w := range wins[1:] {
		last := &out[len(out)-1]
		if w.Start <= last.End+1 {
			last.End = max(last.End, w.End)
			if priority(w.Reason) > priority(last.Reason) {
				last.Reason = w.Reason
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

func priority(reason string) int {
	switch reason {
	case "rejection":
		return 3
	case "correction":
		return 2
	case "repeated":
		return 1
	default: // tool_error
		return 0
	}
}

// capWindows enforces maxWindows, dropping lowest-priority windows first while
// keeping the survivors in chronological order.
func capWindows(wins []Window) []Window {
	for len(wins) > maxWindows {
		drop, dropPri := -1, 99
		for i, w := range wins {
			if p := priority(w.Reason); p < dropPri {
				drop, dropPri = i, p
			}
		}
		wins = append(wins[:drop], wins[drop+1:]...)
	}
	return wins
}

// Render serializes windows for the miner prompt, hard-capped at maxBytes.
// Entry text and tool errors are truncated per-entry so one giant paste cannot
// crowd out every other window.
func Render(entries []Entry, wins []Window, maxBytes int) string {
	var b strings.Builder
	for wi, w := range wins {
		var wb strings.Builder
		fmt.Fprintf(&wb, "--- window %d (%s) ---\n", wi+1, w.Reason)
		for i := w.Start; i <= w.End; i++ {
			e := entries[i]
			if e.Text != "" {
				fmt.Fprintf(&wb, "%s: %s\n", e.Role, truncate(e.Text, perEntryChars))
			}
			for _, te := range e.ToolErrors {
				fmt.Fprintf(&wb, "tool_error: %s\n", truncate(te, perToolErrChars))
			}
		}
		if b.Len()+wb.Len() > maxBytes {
			b.WriteString("--- (further windows truncated) ---\n")
			break
		}
		b.WriteString(wb.String())
	}
	return b.String()
}

// truncate cuts s at n bytes on a rune boundary, single-lining newlines first.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ¶ ")
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// normalize folds, strips apostrophes, and collapses punctuation to single
// spaces so phrase matching is stable across "don't"/"dont"/"DON'T,".
func normalize(s string) string {
	f := retrieve.Fold(s)
	var b strings.Builder
	b.Grow(len(f))
	space := false
	for _, r := range f {
		switch {
		case r == '\'' || r == '’':
			continue
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}
