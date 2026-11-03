package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallJSONKeepsNeighbours(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	in := `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "/usr/local/bin/culi statusline"},
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "/home/u/.local/bin/culi hook user-prompt-submit"}]},
      {"hooks": [{"type": "command", "command": "my-linter --fast"}]}
    ],
    "SessionStart": [
      {"hooks": [
        {"type": "command", "command": "/home/u/.local/bin/culi hook session-start"},
        {"type": "command", "command": "echo hi"}
      ]}
    ],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "'/Users/me/my apps/culi' hook session-end --harness=codex"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := uninstallJSON(path, true)
	if err != nil || n != 4 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "opus" || got["statusLine"] != nil {
		t.Fatalf("top level: %v", got)
	}
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["SessionEnd"]; ok {
		t.Error("event left with only culi hooks should be dropped")
	}
	if ups := hooks["UserPromptSubmit"].([]any); len(ups) != 1 {
		t.Errorf("UserPromptSubmit groups = %v", ups)
	}
	ss := hooks["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["command"] != "echo hi" {
		t.Errorf("SessionStart = %v", ss)
	}
	if _, err := os.Stat(path + ".culi-uninstall-backup"); err != nil {
		t.Errorf("backup missing: %v", err)
	}

	// A second run finds nothing and writes nothing.
	if n, err := uninstallJSON(path, true); err != nil || n != 0 {
		t.Fatalf("second run removed %d, err %v", n, err)
	}
}

func TestUninstallJSONMissingFile(t *testing.T) {
	if n, err := uninstallJSON(filepath.Join(t.TempDir(), "nope.json"), true); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestIsCuliCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/usr/local/bin/culi hook user-prompt-submit": true,
		"culi statusline":                 true,
		`"/opt/my tools/culi" statusline`: true,
		"/bin/culinary serve":             false,
		"ccstatusline":                    false,
		"node /x/other-plugin/hook.js":    false,
	} {
		if got := isCuliCommand(cmd); got != want {
			t.Errorf("isCuliCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestCodexMCPIsCuli(t *testing.T) {
	if !codexMCPIsCuli([]byte(`{"transport":{"type":"stdio","command":"/x/culi","args":["mcp"]}}`)) {
		t.Error("culi transport not recognised")
	}
	if codexMCPIsCuli([]byte(`{"command":"/x/other-server"}`)) {
		t.Error("foreign server treated as culi")
	}
}

func TestPrintNextStepsEmptyStore(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CULI_HOME", base)
	var buf strings.Builder
	printNextSteps(&buf, base)
	out := buf.String()
	for _, want := range []string{"culi import scan", "culi doctor", "culi uninstall", "ollama:"} {
		if !strings.Contains(out, want) {
			t.Errorf("next steps missing %q:\n%s", want, out)
		}
	}
	t.Log("\n" + out)
}
