package retrieve

import (
	"strings"
)

// GateDecision says whether retrieval should run at all. The gate is the
// biggest token saver: 40-60% of real prompts are acks, follow-ups, or slash
// commands that need zero new context.
type GateDecision struct {
	Skip   bool
	Reason string // ack | short | slash | paste | novelty | "" (retrieve)
	Query  string // prompt possibly truncated for huge pastes
}

// ackLexicon holds folded acknowledgement phrases, matched against the whole
// normalized prompt (never substrings). Built-in language packs ship below —
// they coexist safely because a user never types another language's ack by
// accident. New languages are one-PR additions; users extend theirs via
// config `extra_acks` (see ExtendLexicon).
var ackLexicon = map[string]bool{}

var ackPacks = [][]string{
	// en
	{"yes", "y", "no", "ok", "okay", "k", "kk", "continue", "go ahead",
		"do it", "sounds good", "thanks", "thank you", "lgtm", "proceed",
		"sure", "yep", "yeah", "nope", "nice", "good", "great", "done",
		"next", "go on", "keep going", "looks good", "approved", "ship it"},
	// vi
	{"ừ", "ờ", "ừm", "dạ", "vâng", "được", "ok đó", "làm đi", "tiếp tục",
		"tiếp đi", "tiếp", "đúng rồi", "được đó", "cảm ơn", "tốt", "đồng ý",
		"làm luôn", "ok luôn", "chuẩn", "chuẩn rồi"},
}

func init() {
	for _, pack := range ackPacks {
		for _, p := range pack {
			ackLexicon[fold(p)] = true
		}
	}
}

// Budget knobs for the paste detector.
const (
	pasteThreshold = 4000 // prompt bytes before truncation kicks in
	pasteHead      = 800  // human intent lives at the edges of a log dump
	pasteTail      = 400
	minPromptLen   = 12
	noveltyJaccard = 0.6 // > this vs the previous prompt ⇒ same topic, skip
)

// Gate decides skip-vs-retrieve for one prompt. lastPrompt is the previous
// prompt in this session ("" if none) for the novelty check.
func Gate(prompt, lastPrompt string) GateDecision {
	trimmed := strings.TrimSpace(prompt)

	if strings.HasPrefix(trimmed, "/") {
		return GateDecision{Skip: true, Reason: "slash"}
	}
	norm := fold(strings.Trim(trimmed, ".!?,;:"))
	if ackLexicon[norm] {
		return GateDecision{Skip: true, Reason: "ack"}
	}
	if len(trimmed) < minPromptLen {
		return GateDecision{Skip: true, Reason: "short"}
	}

	query := trimmed
	if len(trimmed) > pasteThreshold {
		if logShaped(trimmed) && !strings.Contains(edgeText(trimmed), "?") {
			return GateDecision{Skip: true, Reason: "paste"}
		}
		query = edgeText(trimmed)
	}

	if lastPrompt != "" {
		if jaccard(terms(query, 0), terms(lastPrompt, 0)) > noveltyJaccard {
			return GateDecision{Skip: true, Reason: "novelty"}
		}
	}
	return GateDecision{Query: query}
}

// edgeText returns head+tail of a huge prompt — where human-written intent
// almost always lives in a log dump.
func edgeText(s string) string {
	if len(s) <= pasteHead+pasteTail {
		return s
	}
	return s[:pasteHead] + "\n…\n" + s[len(s)-pasteTail:]
}

// logShaped reports whether >60% of lines look like log/stacktrace output:
// leading timestamps, "at ...", hex addresses, error prefixes, indented frames.
func logShaped(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) < 5 {
		return false
	}
	shaped := 0
	for _, l := range lines {
		if isLogLine(l) {
			shaped++
		}
	}
	return shaped*10 > len(lines)*6
}

func isLogLine(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "Error") ||
		strings.HasPrefix(t, "panic:") || strings.HasPrefix(t, "goroutine ") ||
		strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "#") ||
		strings.HasPrefix(t, "File \"") || strings.HasPrefix(t, "Traceback") {
		return true
	}
	// Leading timestamp-ish: starts with 4 digits ("2026-...") or "[12:34".
	if len(t) >= 5 {
		if isDigits(t[:4]) {
			return true
		}
		if t[0] == '[' && isDigits(t[1:3]) {
			return true
		}
	}
	// Indented continuation frames ("\t/path/file.go:42").
	return strings.HasPrefix(l, "\t") || strings.HasPrefix(l, "    ")
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
