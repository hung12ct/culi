// Package harness names the coding agents culi integrates with (Claude Code,
// Codex CLI) and owns their identity: the typed enum, its validation, and the
// session-id encoding shared by the hook, learn, and serve layers.
//
// This package is stdlib-only on purpose — it is imported by the hot path
// (hook, store), so the C2 dependency firewall applies. Do not add heavy deps.
package harness

import "strings"

// Harness identifies the coding-agent front end a session came from. It is a
// string-backed named type (not an int/iota enum) on purpose: the value is
// serialized verbatim to JSON job files (queue.Job.Source), embedded in
// session-id prefixes, and folded into the learn dedup sha256 key. Keeping the
// underlying type a string means the wire form stays exactly "claude"/"codex"
// with zero custom (un)marshaling, so on-disk and hash compatibility hold.
type Harness string

const (
	Claude Harness = "claude"
	Codex  Harness = "codex"
)

// Default is assumed when no harness is recorded: legacy job files written
// before the field existed, and Claude hooks that omit the --harness flag.
const Default = Claude

// All is the registry of known harnesses. Extend it (plus the typed switches in
// mine's parser routing and init's installer dispatch) to add a harness.
var All = []Harness{Claude, Codex}

// sep separates the harness prefix from the raw session id in a namespaced id.
const sep = ":"

// Parse converts an untrusted string to a Harness, reporting whether it names a
// known harness. Unknown input yields ("", false) — callers decide whether to
// fall back to Default or reject.
func Parse(s string) (Harness, bool) {
	h := Harness(s)
	if h.Valid() {
		return h, true
	}
	return "", false
}

// Valid reports whether h is a known harness.
func (h Harness) Valid() bool {
	switch h {
	case Claude, Codex:
		return true
	}
	return false
}

// String returns the wire form ("claude"/"codex").
func (h Harness) String() string { return string(h) }

// Label returns a human-facing name for the review console.
func (h Harness) Label() string {
	switch h {
	case Claude:
		return "Claude Code"
	case Codex:
		return "Codex CLI"
	}
	return string(h)
}

// PrefixSession namespaces a raw session id with the harness ("codex:<uuid>"),
// so two harnesses' ids never collide in the dedup/session tables. An empty id
// stays empty (the hook only prefixes non-empty ids).
func (h Harness) PrefixSession(id string) string {
	if id == "" {
		return ""
	}
	return string(h) + sep + id
}

// SplitSession is the inverse of PrefixSession: it recovers the harness and raw
// id from a namespaced session id. A missing or unrecognized prefix yields
// (Default, prefixed) — the whole string is preserved untouched.
func SplitSession(prefixed string) (Harness, string) {
	if i := strings.IndexByte(prefixed, ':'); i > 0 {
		if h, ok := Parse(prefixed[:i]); ok {
			return h, prefixed[i+1:]
		}
	}
	return Default, prefixed
}
