// Package knowledge defines the card model and parses the canonical file
// store. Files are the source of truth; everything here must parse
// permissively — one bad card is logged and skipped, never fatal.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

// Card is one atomic unit of knowledge (a rule, lesson, style note, pattern,
// skill, or agent definition) with three granularities: HookLine (~15 tok),
// Summary (~60 tok), Body (full markdown).
type Card struct {
	ID       string // derived from path: "rules/go-error-wrapping"
	Path     string // relative to the knowledge dir
	Type     string // rule|skill|lesson|style|pattern|agent
	Title    string
	Summary  string
	Body     string
	Scopes   []string // global | lang:<x> | repo:<name> | branch:<repo>@<glob> | dir:<hash>
	Key      string   // optional shadowing key: narrowest scope wins among cards sharing it
	Triggers Triggers
	Aliases  []string // extra FTS keywords (e.g. Vietnamese terms)
	Baseline bool     // part of the SessionStart baseline for its scope
	Status   string   // ""/confirmed | candidate | retired (learning lifecycle)

	// Learning lifecycle (Phase 4): a mined card starts as a candidate with
	// Observations 1 and confirms at 2 (re-learning reinforces instead of
	// duplicating). Supersedes names the card ID this one replaces; the old
	// card is retired when this one confirms.
	Observations int
	Supersedes   string

	ContentHash string // sha256 of raw file content
	TokSummary  int    // estimated tokens, computed at parse time
	TokBody     int

	// Export links a card back to a generated ~/.claude artifact; nil for
	// cards that are knowledge-only. Provenance records where imported or
	// learned content came from. Neither is stored in the SQLite index —
	// files are truth, and only `culi import`/`culi export` read them.
	Export     *ExportMeta
	Provenance *Provenance
}

// ExportMeta describes how `culi export` regenerates a Claude Code agent or
// skill file from this card. Frontmatter is the verbatim YAML block of the
// original artifact (without --- delimiters) so exports round-trip exactly.
type ExportMeta struct {
	Kind        string   `yaml:"kind"` // agent | skill
	Name        string   `yaml:"name"` // basename in ~/.claude/agents or ~/.claude/skills
	Frontmatter string   `yaml:"frontmatter,omitempty"`
	Attachments []string `yaml:"attachments,omitempty"` // skill sibling files, dir-relative
}

// Provenance records the origin of imported or learned content.
type Provenance struct {
	Source     string   `yaml:"source,omitempty"`      // import | learn | manual
	MergedFrom []string `yaml:"merged_from,omitempty"` // contributing repo names
	Model      string   `yaml:"model,omitempty"`       // LLM used, if any
}

// Triggers pin a card to the top of the ranked list when matched (max 2 pins
// per injection, enforced by the retriever).
type Triggers struct {
	Keywords []string `yaml:"keywords,omitempty"`
	Globs    []string `yaml:"globs,omitempty"`
}

// IsZero lets yaml omitempty drop an empty triggers block when rendering.
func (t Triggers) IsZero() bool { return len(t.Keywords) == 0 && len(t.Globs) == 0 }

// NarrowestScopeRank returns the precedence rank of the card's narrowest
// in-scope scope: branch(3) > repo(2) > lang(1) > global/dir(0). Used for
// key-shadowing resolution.
func (c Card) NarrowestScopeRank(allowed map[string]bool) int {
	best := -1
	for _, s := range c.Scopes {
		if !allowed[s] {
			continue
		}
		if r := scopeRank(s); r > best {
			best = r
		}
	}
	return best
}

func scopeRank(scope string) int {
	switch {
	case len(scope) > 7 && scope[:7] == "branch:":
		return 3
	case len(scope) > 5 && scope[:5] == "repo:":
		return 2
	case len(scope) > 5 && scope[:5] == "lang:":
		return 1
	default: // global, dir:<hash>
		return 0
	}
}

// EstimateTokens approximates token count without a tokenizer dependency:
// chars/4 for ASCII-heavy text, runes/1.8 when >20% multibyte (Vietnamese is
// token-denser). ±15% accuracy is fine — this drives budgets, not billing.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	bytes := len(s)
	runes := utf8.RuneCountInString(s)
	multibyte := bytes - runes // each multibyte rune contributes ≥1 extra byte
	if runes > 0 && multibyte*5 > runes {
		return runes*10/18 + 1 // runes/1.8
	}
	return bytes/4 + 1
}

// hashContent returns the hex sha256 of raw file bytes.
func hashContent(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ShortID returns a stable 4-hex-char pointer ID derived from the card ID.
// Collisions are resolved at index time (store.uniqueShortID appends a
// decimal suffix deterministically).
func ShortID(cardID string) string {
	sum := sha256.Sum256([]byte(cardID))
	return hex.EncodeToString(sum[:2])
}
