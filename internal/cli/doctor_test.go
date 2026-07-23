package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/learn/codexscan"
)

func TestCodexMCPConfiguredForms(t *testing.T) {
	cases := map[string]string{
		"block":          "[mcp_servers.culi]\ncommand = \"/bin/culi\"\nargs = [\"mcp\"]\n",
		"header-comment": "[mcp_servers.culi]  # added by culi init\ncommand = \"/bin/culi\"\n",
		"inline-table":   "mcp_servers.culi = { command = \"/bin/culi\", args = [\"mcp\"] }\n",
		"other-server":   "[mcp_servers.other]\ncommand = \"/bin/other\"\n",
	}
	want := map[string]bool{"block": true, "header-comment": true, "inline-table": true, "other-server": false}
	for name, body := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := codexMCPConfigured(path); got != want[name] {
			t.Errorf("%s: codexMCPConfigured=%v, want %v", name, got, want[name])
		}
	}
}

func TestFormatCodexScanHealth(t *testing.T) {
	if got := formatCodexScanHealth(codexscan.Health{}, nil); !strings.Contains(got, "never") {
		t.Fatalf("empty health = %q", got)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	h := codexscan.Health{
		LastAttempt: now, LastSuccess: now, Mode: "auto",
		Discovered: 5, Queued: 2, Skipped: 1, DurationMS: 42,
	}
	if got := formatCodexScanHealth(h, nil); !strings.Contains(got, "auto ok") || !strings.Contains(got, "5 discovered, 2 queued") {
		t.Fatalf("success health = %q", got)
	}
	h.Error = "state database busy"
	if got := formatCodexScanHealth(h, nil); !strings.Contains(got, "failed") || !strings.Contains(got, "last success") {
		t.Fatalf("failed health = %q", got)
	}
	if got := formatCodexScanHealth(codexscan.Health{}, errors.New("bad json")); !strings.Contains(got, "health unreadable") {
		t.Fatalf("bad health = %q", got)
	}
}

func TestCodexDoctorHelpers(t *testing.T) {
	home := t.TempDir()
	hooks := `{"hooks":{
"SessionStart":[{"hooks":[{"command":"/bin/culi hook session-start --harness=codex","timeout":10}]}],
"UserPromptSubmit":[{"hooks":[{"command":"/bin/culi hook user-prompt-submit --harness=codex","timeout":10}]}],
"Stop":[{"hooks":[{"command":"/bin/culi hook stop --harness=codex","timeout":30}]}],
"SessionEnd":[{"hooks":[{"command":"/bin/culi hook session-end --harness=codex","timeout":3}]}]}}`
	hookPath := filepath.Join(home, "hooks.json")
	if err := os.WriteFile(hookPath, []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, ok := codexHookStatus(hookPath); n != 4 || !ok {
		t.Fatalf("hooks=%d timeoutOK=%v", n, ok)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("[mcp_servers.culi]\ncommand = \"/bin/culi\"\nargs = [\"mcp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !codexMCPConfigured(configPath) {
		t.Fatal("configured MCP not detected")
	}

	logPath := filepath.Join(home, "hook.log")
	log := "2026-07-23T00:00:00Z gate skip (short) session=claude:x\n" +
		"2026-07-23T00:01:00Z hook stop queued session=codex:y\n"
	if err := os.WriteFile(logPath, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lastCodexHook(logPath); !strings.Contains(got, "hook stop queued session=codex:y") {
		t.Fatalf("last=%q", got)
	}

	inbox := filepath.Join(home, "inbox")
	if err := os.Mkdir(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "one.json"), []byte(`{"source":"codex","transcript_path":"/tmp/roll.jsonl"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "bad.json"), []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pendingCodexJobs(inbox); got != 1 {
		t.Fatalf("pending=%d", got)
	}
	if _, err := os.Stat(filepath.Join(inbox, "bad.json")); err != nil {
		t.Fatalf("doctor mutated malformed job: %v", err)
	}
}
