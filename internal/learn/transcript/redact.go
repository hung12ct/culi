package transcript

import (
	"regexp"
	"strings"
)

// Redacted replaces content removed before a transcript reaches a learning
// model.
const Redacted = "[redacted]"

var (
	// privateBlock matches <private>…</private>, case-insensitive and across
	// lines. An unclosed tag hides everything after it: failing closed is the
	// only safe reading of a half-marked secret.
	privateBlock = regexp.MustCompile(`(?is)<private>.*?(?:</private>|\z)`)

	// secretPatterns are credential shapes with a recognizable prefix, so the
	// false-positive rate on prose and code stays near zero.
	secretPatterns = regexp.MustCompile(strings.Join([]string{
		`sk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`, // Anthropic / OpenAI keys
		`gh[pousr]_[A-Za-z0-9]{30,}`,           // GitHub tokens
		`github_pat_[A-Za-z0-9_]{40,}`,         // GitHub fine-grained tokens
		`AKIA[0-9A-Z]{16}`,                     // AWS access key IDs
		`xox[abprs]-[A-Za-z0-9-]{10,}`,         // Slack tokens
		`AIza[0-9A-Za-z_-]{35}`,                // Google API keys
		`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`,
		`(?i)bearer\s+[A-Za-z0-9._~+/=-]{20,}`,
	}, "|"))
)

// Redact strips <private> blocks and masks credential-shaped strings. It runs
// on every entry a reader returns, so mining, style synthesis and usage
// attribution all see the same redacted text, and nothing marked private is
// ever sent to a learning backend.
func Redact(s string) string {
	if strings.Contains(s, "<") && strings.Contains(strings.ToLower(s), "<private>") {
		s = privateBlock.ReplaceAllString(s, Redacted)
	}
	return secretPatterns.ReplaceAllString(s, Redacted)
}

// RedactEntry applies Redact to an entry's text and tool errors.
func RedactEntry(e Entry) Entry {
	e.Text = Redact(e.Text)
	for i, te := range e.ToolErrors {
		e.ToolErrors[i] = Redact(te)
	}
	return e
}
