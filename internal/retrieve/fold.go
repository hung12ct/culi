package retrieve

import (
	"strings"
	"unicode"
)

// latinFold maps Latin diacritic letters to their base letters — lowercase
// only (fold callers lowercase first). Built from compact per-base strings so
// contributions for more languages are one-line additions. This mirrors
// FTS5's `remove_diacritics 2`, keeping Go-side matching (ack lexicon,
// Jaccard, matched-term counting) in agreement with SQL-side matching.
//
// Coverage: Latin-1 Supplement + Latin Extended-A + Vietnamese (Latin
// Extended Additional) — French, German, Spanish, Portuguese, Italian,
// Nordic, Polish, Czech, Turkish, Romanian, Vietnamese, …
var latinFold = map[rune]rune{}

func init() {
	for base, variants := range map[rune]string{
		'a': "àáâãäåāăąạảấầẩẫậắằẳẵặ",
		'c': "çćĉċč",
		'd': "ďđ",
		'e': "èéêëēĕėęěẹẻẽếềểễệ",
		'g': "ĝğġģ",
		'h': "ĥħ",
		'i': "ìíîïĩīĭįıỉị",
		'j': "ĵ",
		'k': "ķ",
		'l': "ĺļľŀł",
		'n': "ñńņňŉ",
		'o': "òóôõöøōŏőọỏốồổỗộớờởỡợơ",
		'r': "ŕŗř",
		's': "śŝşš",
		't': "ţťŧ",
		'u': "ùúûüũūŭůűųưụủứừửữự",
		'w': "ŵ",
		'y': "ýÿŷỳỵỷỹ",
		'z': "źżž",
	} {
		for _, v := range variants {
			latinFold[v] = base
		}
	}
}

// fold lowercases and strips Latin diacritics.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if r == 'ß' { // the one multi-char fold (de): ß → ss
			b.WriteString("ss")
			continue
		}
		if base, ok := latinFold[r]; ok {
			r = base
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stopwords excluded from term extraction. Deliberately tiny per language —
// aggressive stopword lists hurt short technical prompts. Language packs live
// here (folded forms); users extend via config `extra_stopwords`.
var stopwords = map[string]bool{}

var stopwordPacks = [][]string{
	// en
	{"the", "a", "an", "and", "or", "of", "to", "in", "on", "for", "with",
		"is", "it", "this", "that", "be", "at", "as"},
	// vi (folded)
	{"la", "va", "cua", "cho", "voi", "trong", "mot", "cai", "nay", "thi", "o"},
}

func init() {
	for _, pack := range stopwordPacks {
		for _, w := range pack {
			stopwords[fold(w)] = true
		}
	}
}

// ExtendLexicon adds user-configured ack phrases and stopwords (from
// config.yaml) to the built-in packs. Called once at startup before any Gate
// call; not safe to call concurrently with retrieval.
func ExtendLexicon(acks, stops []string) {
	for _, p := range acks {
		if f := fold(strings.TrimSpace(p)); f != "" {
			ackLexicon[f] = true
		}
	}
	for _, w := range stops {
		if f := fold(strings.TrimSpace(w)); f != "" {
			stopwords[f] = true
		}
	}
}

// terms extracts folded content words: unicode letters/digits runs, ≥2 runes,
// stopwords dropped, capped at max (0 = unlimited).
func terms(s string, max int) []string {
	folded := fold(s)
	var out []string
	seen := map[string]bool{}
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		w := folded[start:end]
		start = -1
		if len(w) < 2 || stopwords[w] || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for i, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
		if max > 0 && len(out) >= max {
			return out
		}
	}
	flush(len(folded))
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// Fold, Terms and Jaccard export the text-normalization primitives for
// non-hot-path consumers (the learning pipelines) so Go-side matching agrees
// everywhere. The hot path keeps calling the unexported forms directly.
func Fold(s string) string             { return fold(s) }
func Terms(s string, max int) []string { return terms(s, max) }
func Jaccard(a, b []string) float64    { return jaccard(a, b) }

// jaccard computes set overlap of two term slices.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, w := range a {
		set[w] = true
	}
	inter := 0
	union := len(set)
	for _, w := range b {
		if set[w] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}
