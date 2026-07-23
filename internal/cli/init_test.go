package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterHooksCodexShapeAndIdempotency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	original := `{"description":"keep","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"check"}]}]}}`
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := registerHooksCodex()
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "hooks.json"))
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["description"] != "keep" {
		t.Errorf("unknown top-level field lost: %v", root)
	}
	hooks := root["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd", "PreToolUse"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("missing event %s", event)
		}
	}
	if !strings.Contains(string(raw), "--harness=codex") {
		t.Error("Codex adapter identity missing")
	}
	if got := culiHookTimeout(hooks["SessionEnd"]); got != 3 {
		t.Errorf("SessionEnd timeout = %v, want 3", got)
	}
	backup, err := os.ReadFile(filepath.Join(home, "hooks.json.culi-backup"))
	if err != nil || string(backup) != original {
		t.Errorf("backup=%q err=%v", backup, err)
	}
	changed, err = registerHooksCodex()
	if err != nil || changed {
		t.Fatalf("second changed=%v err=%v", changed, err)
	}
}

func TestRegisterHooksCodexRepairsOldSessionEndTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	old := `{"hooks":{"SessionEnd":[{"hooks":[
{"type":"command","command":"/bin/culi hook session-end --harness=codex","timeout":30},
{"type":"command","command":"keep-me","timeout":19}
]}]}}`
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := registerHooksCodex()
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "hooks.json"))
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	if got := culiHookTimeout(hooks["SessionEnd"]); got != 3 {
		t.Fatalf("SessionEnd timeout = %v, want 3", got)
	}
	if !strings.Contains(string(raw), `"command": "keep-me"`) || !strings.Contains(string(raw), `"timeout": 19`) {
		t.Fatalf("unrelated hook changed: %s", raw)
	}
}

func TestSelectHarnessesAndLegacyMCPJSON(t *testing.T) {
	if got, err := selectHarnesses("all"); err != nil || strings.Join(got, ",") != "claude,codex" {
		t.Fatalf("all=%v err=%v", got, err)
	}
	if _, err := selectHarnesses("wat"); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if !codexMCPMatches([]byte(`{"command":"/bin/culi","args":["mcp"]}`), "/bin/culi") {
		t.Error("matching MCP JSON rejected")
	}
	if !codexMCPMatches([]byte(`{"transport":{"type":"stdio","command":"/bin/culi","args":["mcp"]}}`), "/bin/culi") {
		t.Error("current nested MCP JSON rejected")
	}
}
