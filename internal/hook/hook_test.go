package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points CULI_HOME at a temp dir and seeds a small knowledge store.
// The learn-worker spawn is disabled: under `go test`, os.Executable is the
// test binary, and re-executing it would fork this whole suite as a rogue
// concurrent child (the exact flake CI caught).
func sandbox(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CULI_HOME", base)
	t.Setenv("CULI_NO_LEARN_SPAWN", "1")
	writeCard(t, base, "rules/go-errors.md", `---
title: Wrap Go errors
scope: [global]
summary: Wrap errors with a package prefix using fmt.Errorf and %w.
baseline: true
---
Every error crossing a package boundary is wrapped: fmt.Errorf("pkg: doing X: %w", err).
`)
	writeCard(t, base, "rules/webhook.md", `---
title: Webhook retries
scope: [global]
summary: Webhook handlers must be idempotent because retries redeliver events.
---
Use an idempotency key on webhook processing; retries are at-least-once.
`)
	return base
}

func writeCard(t *testing.T, base, rel, content string) {
	t.Helper()
	full := filepath.Join(base, "knowledge", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runHook(t *testing.T, event string, stdin string) (stdout string, code int) {
	t.Helper()
	var out bytes.Buffer
	code = Run([]string{event}, strings.NewReader(stdin), &out)
	return out.String(), code
}

func hookInput(sessionID, prompt string) string {
	raw, _ := json.Marshal(Input{
		SessionID: sessionID, CWD: "/tmp", Prompt: prompt,
		HookEventName: "UserPromptSubmit",
	})
	return string(raw)
}

// index seeds the DB via a session-start event (which runs the inline sync).
func index(t *testing.T, sessionID string) string {
	t.Helper()
	raw, _ := json.Marshal(Input{SessionID: sessionID, CWD: "/tmp", Source: "startup"})
	out, code := runHook(t, "session-start", string(raw))
	if code != 0 {
		t.Fatalf("session-start exit = %d", code)
	}
	return out
}

func TestFailOpenContract(t *testing.T) {
	sandbox(t)
	cases := []struct {
		name, event, stdin string
	}{
		{"malformed json", "user-prompt-submit", "not-json{{{"},
		{"empty stdin", "user-prompt-submit", ""},
		{"unknown event", "definitely-not-an-event", hookInput("s", "p")},
		{"missing fields", "user-prompt-submit", "{}"},
	}
	for _, tc := range cases {
		out, code := runHook(t, tc.event, tc.stdin)
		if code != 0 {
			t.Errorf("%s: exit = %d, want 0", tc.name, code)
		}
		if out != "" {
			t.Errorf("%s: stdout = %q, want empty", tc.name, out)
		}
	}
	// No args at all.
	var buf bytes.Buffer
	if code := Run(nil, strings.NewReader(""), &buf); code != 0 || buf.Len() != 0 {
		t.Errorf("no-args: code=%d out=%q", code, buf.String())
	}
}

func TestFailOpenWithoutStore(t *testing.T) {
	// CULI_HOME pointing at a read-only location: store open fails → fail open.
	t.Setenv("CULI_HOME", "/dev/null/impossible")
	out, code := runHook(t, "user-prompt-submit", hookInput("s", "how do I wrap errors in this project?"))
	if code != 0 || out != "" {
		t.Errorf("store-less hook: code=%d out=%q", code, out)
	}
}

func TestSessionStartInjectsBaseline(t *testing.T) {
	sandbox(t)
	out := index(t, "sess-base")
	if out == "" {
		t.Fatal("session-start injected nothing; baseline card expected")
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output not JSON: %v: %q", err, out)
	}
	hso := resp["hookSpecificOutput"].(map[string]any)
	if hso["hookEventName"] != "SessionStart" {
		t.Errorf("hookEventName = %v", hso["hookEventName"])
	}
	ctxText := hso["additionalContext"].(string)
	if !strings.Contains(ctxText, "Wrap Go errors") || !strings.Contains(ctxText, "<ctx>") {
		t.Errorf("baseline content missing: %q", ctxText)
	}
}

func TestPromptInjectionAndDedup(t *testing.T) {
	sandbox(t)
	index(t, "sess-1")

	// Relevant prompt: webhook card injected.
	out, code := runHook(t, "user-prompt-submit", hookInput("sess-1", "add retry handling to the webhook endpoint"))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "Webhook retries") {
		t.Fatalf("webhook card not injected: %q", out)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	// Same prompt again: novelty gate (or dedup) ⇒ nothing.
	out, _ = runHook(t, "user-prompt-submit", hookInput("sess-1", "add retry handling to the webhook endpoint again"))
	if out != "" {
		t.Errorf("repeat prompt should inject nothing, got %q", out)
	}
}

func TestGateSkipsProduceNoOutput(t *testing.T) {
	sandbox(t)
	index(t, "sess-2")
	for _, prompt := range []string{"ok", "tiếp tục", "/commit", "yes!"} {
		out, code := runHook(t, "user-prompt-submit", hookInput("sess-2", prompt))
		if code != 0 || out != "" {
			t.Errorf("gate prompt %q: code=%d out=%q", prompt, code, out)
		}
	}
}

func TestSessionEndEnqueues(t *testing.T) {
	base := sandbox(t)
	raw, _ := json.Marshal(Input{SessionID: "s-end", TranscriptPath: "/tmp/tr.jsonl", CWD: "/tmp"})
	out, code := runHook(t, "session-end", string(raw))
	if code != 0 || out != "" {
		t.Fatalf("session-end: code=%d out=%q", code, out)
	}
	entries, err := os.ReadDir(filepath.Join(base, "inbox"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("inbox entries = %v, %v", entries, err)
	}
}

func TestCompactResetsDedup(t *testing.T) {
	sandbox(t)
	index(t, "sess-c")
	out, _ := runHook(t, "user-prompt-submit", hookInput("sess-c", "add retry handling to the webhook endpoint"))
	if out == "" {
		t.Fatal("first injection expected")
	}

	// Compact: dedup resets, and the baseline is re-injected.
	raw, _ := json.Marshal(Input{SessionID: "sess-c", CWD: "/tmp", Source: "compact"})
	out, _ = runHook(t, "session-start", string(raw))
	if out == "" {
		t.Fatal("post-compact session-start should re-inject baseline")
	}

	// The webhook card can now inject again (different prompt to pass novelty).
	out, _ = runHook(t, "user-prompt-submit", hookInput("sess-c", "webhook processing needs idempotency handling for retries safety"))
	if !strings.Contains(out, "Webhook retries") {
		t.Errorf("post-compact re-injection failed: %q", out)
	}
}
