package transcript

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"private block", "keep <private>db pass hunter2</private> this", "keep [redacted] this"},
		{"multiline, any case", "a <PRIVATE>x\ny</Private> b", "a [redacted] b"},
		{"unclosed hides the rest", "a <private>leak everything", "a [redacted]"},
		{"openai key", "use sk-proj-abcdefghijklmnopqrstuvwx1234 now", "use [redacted] now"},
		{"anthropic key", "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUV", "[redacted]"},
		{"github token", "ghp_0123456789abcdefghijklmnopqrstuvwxyz", "[redacted]"},
		{"aws key id", "AKIAIOSFODNN7EXAMPLE", "[redacted]"},
		{"bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "Authorization: [redacted]"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", "[redacted]"},
		{"prose untouched", "skip the ask-first rule; use sk- prefixes in docs", "skip the ask-first rule; use sk- prefixes in docs"},
		{"code untouched", "if task-runner-config { return }", "if task-runner-config { return }"},
	} {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRedactEntryCoversToolErrors(t *testing.T) {
	e := RedactEntry(Entry{Text: "<private>x</private>", ToolErrors: []string{"token ghp_0123456789abcdefghijklmnopqrstuvwxyz rejected"}})
	if e.Text != Redacted || strings.Contains(e.ToolErrors[0], "ghp_") {
		t.Fatalf("entry not redacted: %+v", e)
	}
}
