package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
)

// Uninstall removes culi's hooks, statusline and MCP registrations from Claude
// Code and Codex, leaving every neighbouring entry untouched. The knowledge
// store stays in ~/.culi unless --purge, so a reinstall picks up where the
// user left off.
func Uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete ~/.culi (cards, index, logs) after confirmation")
	yes := fs.Bool("yes", false, "with --purge, skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cli: resolving home: %w", err)
	}

	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	n, err := uninstallJSON(claudeSettings, true)
	if err != nil {
		return err
	}
	fmt.Printf("claude:   removed %d culi hook/statusline entr%s from %s\n", n, plural(n, "y", "ies"), claudeSettings)
	if dir := codexHome(); dir != "" {
		codexHooks := filepath.Join(dir, "hooks.json")
		n, err := uninstallJSON(codexHooks, false)
		if err != nil {
			return err
		}
		fmt.Printf("codex:    removed %d culi hook entr%s from %s\n", n, plural(n, "y", "ies"), codexHooks)
	}
	removeMCPClaude()
	removeMCPCodex()

	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	if !*purge {
		fmt.Printf("\nkept:     %s (your cards and history). Delete it with: culi uninstall --purge\n", base)
	} else if err := purgeStore(base, *yes); err != nil {
		return err
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Printf("binary:   remove it with: rm %s\n", quoteCommandArg(exe))
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// uninstallJSON strips culi entries from one harness JSON file. A missing
// file is a no-op. The original is backed up once before the first change.
func uninstallJSON(path string, statusLine bool) (int, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cli: reading %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}
	root := map[string]any{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return 0, fmt.Errorf("cli: %s is not valid JSON, edit it by hand: %w", path, err)
	}
	removed := stripCuliHooks(root)
	if statusLine {
		if sl, _ := root["statusLine"].(map[string]any); sl != nil {
			if cmd, _ := sl["command"].(string); isCuliCommand(cmd) {
				delete(root, "statusLine")
				removed++
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}
	backup := path + ".culi-uninstall-backup"
	if err := os.WriteFile(backup, raw, 0o644); err != nil {
		return 0, fmt.Errorf("cli: writing backup %s: %w", backup, err)
	}
	if err := atomicWriteJSON(path, root, ".culi-uninstall-*"); err != nil {
		return 0, err
	}
	return removed, nil
}

// stripCuliHooks removes culi-owned commands from root["hooks"], dropping
// matcher groups and events that end up empty. It returns how many commands
// it removed.
func stripCuliHooks(root map[string]any) int {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	removed := 0
	for event, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			n, empty := stripGroup(g)
			removed += n
			if !empty {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}
	return removed
}

// stripGroup removes culi commands from one matcher group in place. empty is
// true when the group held only culi commands and should be dropped.
func stripGroup(g any) (removed int, empty bool) {
	gm, ok := g.(map[string]any)
	if !ok {
		return 0, false
	}
	inner, _ := gm["hooks"].([]any)
	keep := make([]any, 0, len(inner))
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		if isCuliCommand(cmd) {
			removed++
			continue
		}
		keep = append(keep, h)
	}
	if removed == 0 {
		return 0, false
	}
	gm["hooks"] = keep
	return removed, len(keep) == 0
}

// isCuliCommand reports whether a hook or statusline command runs culi. The
// second clause covers Codex commands whose quoted path contains spaces.
func isCuliCommand(cmd string) bool {
	return filepath.Base(commandExe(cmd)) == "culi" ||
		(strings.Contains(cmd, " hook ") && strings.Contains(cmd, "--harness=codex"))
}

// commandExe returns the program of a shell command line, honouring a leading
// single- or double-quoted path that may contain spaces.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd != "" && (cmd[0] == '\'' || cmd[0] == '"') {
		if end := strings.IndexByte(cmd[1:], cmd[0]); end >= 0 {
			return cmd[1 : end+1]
		}
	}
	return firstField(cmd)
}

// removeMCPClaude asks the claude CLI, which owns ~/.claude.json, to drop the
// user-scope culi server. Best-effort, like registration.
func removeMCPClaude() {
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("mcp:      claude CLI not found; if culi is registered, run: claude mcp remove --scope user culi")
		return
	}
	if exec.Command(claude, "mcp", "get", "culi").Run() != nil {
		fmt.Println("mcp:      claude has no culi server")
		return
	}
	if out, err := exec.Command(claude, "mcp", "remove", "--scope", "user", "culi").CombinedOutput(); err != nil {
		fmt.Printf("warning:  claude mcp remove failed (%v: %s)\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("mcp:      removed culi from claude")
}

// removeMCPCodex drops the Codex entry only when it runs a culi binary, so a
// user's unrelated server that happens to be named culi survives.
func removeMCPCodex() {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return
	}
	out, err := exec.Command(codex, "mcp", "get", "culi", "--json").CombinedOutput()
	if err != nil {
		fmt.Println("mcp:      codex has no culi server")
		return
	}
	if !codexMCPIsCuli(out) {
		fmt.Println("mcp:      codex server named culi does not run culi; left in place")
		return
	}
	if out, err := exec.Command(codex, "mcp", "remove", "culi").CombinedOutput(); err != nil {
		fmt.Printf("warning:  codex mcp remove failed (%v: %s)\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("mcp:      removed culi from codex")
}

func codexMCPIsCuli(raw []byte) bool {
	var cfg struct {
		Command   string `json:"command"`
		Transport struct {
			Command string `json:"command"`
		} `json:"transport"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	cmd := cfg.Transport.Command
	if cmd == "" {
		cmd = cfg.Command
	}
	return filepath.Base(cmd) == "culi"
}

// purgeStore deletes the culi home after an explicit confirmation.
func purgeStore(base string, yes bool) error {
	if !yes {
		fmt.Printf("\nDelete %s and every card, index and log in it? Type 'delete' to confirm: ", base)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "delete" {
			fmt.Println("kept:     nothing deleted")
			return nil
		}
	}
	if err := os.RemoveAll(base); err != nil {
		return fmt.Errorf("cli: deleting %s: %w", base, err)
	}
	fmt.Printf("deleted:  %s\n", base)
	return nil
}
