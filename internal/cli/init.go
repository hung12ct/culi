// Package cli implements the non-hook subcommands. One concern per file.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/store"
)

// Init sets up ~/.culi and registers the Claude Code hooks.
func Init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	noHooks := fs.Bool("no-hooks", false, "skip registering hooks in ~/.claude/settings.json")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	for _, dir := range []string{
		filepath.Join(config.KnowledgeDir(base), "rules"),
		filepath.Join(config.KnowledgeDir(base), "styles"),
		filepath.Join(config.KnowledgeDir(base), "patterns"),
		filepath.Join(config.KnowledgeDir(base), "lessons"),
		filepath.Join(config.KnowledgeDir(base), "skills"),
		filepath.Join(config.KnowledgeDir(base), "agents"),
		config.LogDir(base),
		config.InboxDir(base),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cli: creating %s: %w", dir, err)
		}
	}
	fmt.Printf("store:    %s\n", base)

	// knowledge/ is git-init'd so LLM merges and learning are always revertible.
	kdir := config.KnowledgeDir(base)
	if _, err := os.Stat(filepath.Join(kdir, ".git")); errors.Is(err, os.ErrNotExist) {
		if out, err := exec.Command("git", "-C", kdir, "init", "-q").CombinedOutput(); err != nil {
			fmt.Printf("warning:  git init failed (%v: %s) — knowledge history disabled\n", err, out)
		} else {
			fmt.Printf("git:      initialized %s\n", kdir)
		}
	}

	cfgPath := filepath.Join(base, "config.yaml")
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		const defaultCfg = `# culi configuration — see PLAN.md for knob meanings
push_budget: 700       # max tokens injected per prompt
baseline_budget: 1200  # max tokens injected at session start
ollama:
  endpoint: http://localhost:11434
  model: nomic-embed-text
# repos to reconcile with 'culi import' (absolute paths):
# repos:
#   - /path/to/repo-a
#   - /path/to/repo-b
# import:
#   # merge backend: auto (default) picks anthropic when ANTHROPIC_API_KEY is
#   # set, else claude-cli (your Claude subscription, no key). Also: ollama
#   # (free, local - set merge_model to a local model like qwen3) or none.
#   provider: auto
#   merge_model: claude-sonnet-5
`
		if err := os.WriteFile(cfgPath, []byte(defaultCfg), 0o644); err != nil {
			return fmt.Errorf("cli: writing config.yaml: %w", err)
		}
		fmt.Printf("config:   %s\n", cfgPath)
	}

	s, err := store.Open(context.Background(), config.DBPath(base))
	if err != nil {
		return err
	}
	s.Close()
	fmt.Printf("index:    %s\n", config.DBPath(base))

	if *noHooks {
		fmt.Println("hooks:    skipped (--no-hooks)")
		return nil
	}
	changed, err := registerHooks()
	if err != nil {
		return err
	}
	if changed {
		fmt.Println("hooks:    registered in ~/.claude/settings.json (backup: settings.json.culi-backup)")
	} else {
		fmt.Println("hooks:    already registered")
	}
	registerMCP()
	fmt.Println("\nSeed cards into", kdir, "then run `culi index`.")
	return nil
}

// registerMCP registers `culi mcp` as a user-scope MCP server via the claude
// CLI — the CLI owns ~/.claude.json, so we never edit that file ourselves
// (C4). Best-effort: a missing/odd claude binary just prints the manual step.
func registerMCP() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	manual := fmt.Sprintf("mcp:      run: claude mcp add --scope user culi -- %s mcp", exe)
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println(manual)
		return
	}
	if exec.Command(claude, "mcp", "get", "culi").Run() == nil {
		fmt.Println("mcp:      already registered")
		return
	}
	if out, err := exec.Command(claude, "mcp", "add", "--scope", "user", "culi", "--", exe, "mcp").CombinedOutput(); err != nil {
		fmt.Printf("warning:  claude mcp add failed (%v: %s)\n%s\n", err, strings.TrimSpace(string(out)), manual)
		return
	}
	fmt.Println("mcp:      registered (user scope) — tools: search_context, expand_card, save_lesson")
}

// registerHooks merges culi's three hook entries into ~/.claude/settings.json,
// preserving everything else. Never destroys user content (C4): the original
// is backed up once before the first modification.
func registerHooks() (changed bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("cli: resolving home: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("cli: resolving executable: %w", err)
	}
	path := filepath.Join(home, ".claude", "settings.json")

	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("cli: reading settings.json: %w", err)
	default:
		if len(bytes.TrimSpace(raw)) == 0 {
			break // empty file: treat as {}
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			return false, fmt.Errorf("cli: settings.json is not valid JSON — fix it or run with --no-hooks: %w", err)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	events := []struct {
		name    string
		cmd     string
		timeout float64
		async   bool
	}{
		{"SessionStart", exe + " hook session-start", 10, false},
		{"UserPromptSubmit", exe + " hook user-prompt-submit", 10, false},
		{"SessionEnd", exe + " hook session-end", 30, true},
	}
	for _, ev := range events {
		if hasCuliHook(hooks[ev.name]) {
			continue
		}
		entry := map[string]any{"type": "command", "command": ev.cmd, "timeout": ev.timeout}
		if ev.async {
			entry["async"] = true
		}
		group := map[string]any{"hooks": []any{entry}}
		list, _ := hooks[ev.name].([]any)
		hooks[ev.name] = append(list, group)
		changed = true
	}
	if !changed {
		return false, nil
	}

	settings["hooks"] = hooks
	if raw != nil {
		backup := path + ".culi-backup"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backup, raw, 0o644); err != nil {
				return false, fmt.Errorf("cli: writing backup: %w", err)
			}
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("cli: marshaling settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("cli: creating ~/.claude: %w", err)
	}
	// Atomic write (C4): settings.json is the user's most critical Claude Code
	// file — a torn in-place write would break every feature. Temp + rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-culi-*")
	if err != nil {
		return false, fmt.Errorf("cli: creating temp settings: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return false, fmt.Errorf("cli: writing temp settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return false, fmt.Errorf("cli: closing temp settings: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return false, fmt.Errorf("cli: replacing settings.json: %w", err)
	}
	return true, nil
}

// hasCuliHook scans one event's matcher groups for an existing culi command.
func hasCuliHook(v any) bool {
	groups, _ := v.([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if filepath.Base(firstField(cmd)) == "culi" {
				return true
			}
		}
	}
	return false
}

func firstField(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return s[:i]
		}
	}
	return s
}
