package harness

import "testing"

func TestParse(t *testing.T) {
	for _, s := range []string{"claude", "codex"} {
		if h, ok := Parse(s); !ok || h.String() != s {
			t.Errorf("Parse(%q) = %q, %v", s, h, ok)
		}
	}
	for _, s := range []string{"", "gemini", "Claude", "codex "} {
		if h, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %q, want !ok", s, h)
		}
	}
}

func TestDefaultIsClaude(t *testing.T) {
	if Default != Claude {
		t.Fatalf("Default = %q, want claude", Default)
	}
}

func TestPrefixSession(t *testing.T) {
	if got := Codex.PrefixSession("abc"); got != "codex:abc" {
		t.Errorf("PrefixSession = %q", got)
	}
	if got := Claude.PrefixSession(""); got != "" {
		t.Errorf("empty id must stay empty, got %q", got)
	}
}

func TestSplitSession(t *testing.T) {
	// Round-trip both harnesses.
	for _, h := range All {
		prefixed := h.PrefixSession("019f8db8-ed8e")
		gotH, gotID := SplitSession(prefixed)
		if gotH != h || gotID != "019f8db8-ed8e" {
			t.Errorf("SplitSession(%q) = %q, %q", prefixed, gotH, gotID)
		}
	}
	// Unknown or absent prefix falls back to Default and preserves the whole id.
	for _, in := range []string{"raw-uuid-no-prefix", "gemini:xyz", ":leading"} {
		h, id := SplitSession(in)
		if h != Default || id != in {
			t.Errorf("SplitSession(%q) = %q, %q; want %q, whole", in, h, id, Default)
		}
	}
}
