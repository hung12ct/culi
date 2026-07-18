package retrieve

import "strings"

// maxQueryTerms caps the FTS expression size — beyond ~24 terms extra terms
// add noise, not recall.
const maxQueryTerms = 24

// FTSExpr builds a safe FTS5 MATCH expression from raw prompt text. Prompt
// text is hostile input to FTS5 (quotes and operators crash MATCH — 14.42):
// we extract bare folded terms and OR-join them, each double-quoted.
// Returns "" when nothing searchable remains.
func FTSExpr(prompt string) string {
	ts := terms(prompt, maxQueryTerms)
	if len(ts) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteString(" OR ")
		}
		// terms() output is letters/digits only, but quote anyway: defense in
		// depth against future tokenizer changes.
		b.WriteByte('"')
		b.WriteString(t)
		b.WriteByte('"')
	}
	return b.String()
}
