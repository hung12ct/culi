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
	"runtime"
	"strconv"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/store"
)

// Init sets up ~/.culi and registers the selected coding harnesses.
func Init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	noHooks := fs.Bool("no-hooks", false, "skip registering lifecycle hooks (MCP is still registered)")
	harness := fs.String("harness", "auto", "harness to configure: auto|claude|codex|all")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	harnesses, err := selectHarnesses(*harness)
	if err != nil {
		return err
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

	// knowledge/ is git-init'd so LLM merges and learning are always revertible;
	// the pipelines auto-commit with structured messages (the governance trail).
	kdir := config.KnowledgeDir(base)
	if _, err := os.Stat(filepath.Join(kdir, ".git")); errors.Is(err, os.ErrNotExist) {
		if out, err := exec.Command("git", "-C", kdir, "init", "-q").CombinedOutput(); err != nil {
			fmt.Printf("warning:  git init failed (%v: %s) — knowledge history disabled\n", err, out)
		} else {
			fmt.Printf("git:      initialized %s\n", kdir)
		}
	}
	// Staged-but-unreviewed import content is not knowledge yet: keep it out
	// of both auto-commits and any hand-run `git add -A`.
	kIgnore := filepath.Join(kdir, ".gitignore")
	if _, err := os.Stat(kIgnore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(kIgnore, []byte(".import/\n.drafts/\n"), 0o644); err != nil {
			fmt.Printf("warning:  writing knowledge/.gitignore: %v\n", err)
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
# learn:
#   # background transcript mining (lessons/rules/style). Same backend
#   # options as import.provider; both daily caps must pass for a call.
#   enabled: true
#   provider: auto
#   cheap_model: claude-haiku-4-5
#   strong_model: claude-sonnet-5
#   daily_usd_cap: 0.50    # anthropic API only; claude-cli/ollama cost $0
#   daily_call_cap: 40     # all backends - the subscription-quota guard
#   candidate_ttl_days: 30 # auto-retire candidates unreinforced this long;
#                          # -1 disables. Confirmed cards are never auto-retired.
#   max_jobs_per_run: 50   # transcripts mined per learn run, newest first;
#                          # -1 = no limit. learn --no-cap drains everything.
#   confirm_at: 2          # observations to auto-confirm a candidate (no review);
#                          # 1 = confirm on first sighting (noisier), higher = stricter.
#   # Headless-learning auth (pick ONE; both empty/unset = off, ~ expands):
#   # Claude Code doesn't pass its OAuth token or an API key to hook-spawned
#   # processes, so background mining gets "Not logged in". Point culi at a file:
#   #   • subscription (free)   — a file holding CLAUDE_CODE_OAUTH_TOKEN:
#   oauth_token_file: ~/.claude-tokens/account-my.token
#   #   • metered API (paid)    — a file holding ANTHROPIC_API_KEY; also makes
#   #                             provider:auto prefer the API and track real $:
#   anthropic_api_key_file: ~/.anthropic/api-key
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

	if len(harnesses) == 0 {
		fmt.Println("harness:  none detected (use --harness=claude|codex|all to configure explicitly)")
	}
	for _, h := range harnesses {
		if *noHooks {
			fmt.Printf("hooks:    %s skipped (--no-hooks)\n", h)
		} else {
			var changed bool
			switch h {
			case "claude":
				changed, err = registerHooksClaude()
			case "codex":
				changed, err = registerHooksCodex()
			}
			if err != nil {
				return err
			}
			if changed {
				fmt.Printf("hooks:    %s registered\n", h)
			} else {
				fmt.Printf("hooks:    %s already registered\n", h)
			}
		}
		switch h {
		case "claude":
			registerMCPClaude()
		case "codex":
			registerMCPCodex()
		}
	}
	fmt.Println("\nSeed cards into", kdir, "then run `culi index`.")
	return nil
}

func selectHarnesses(v string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "claude":
		return []string{"claude"}, nil
	case "codex":
		return []string{"codex"}, nil
	case "all":
		return []string{"claude", "codex"}, nil
	case "auto":
		var out []string
		home, _ := os.UserHomeDir()
		if _, err := exec.LookPath("claude"); err == nil || dirExists(filepath.Join(home, ".claude")) {
			out = append(out, "claude")
		}
		if _, err := exec.LookPath("codex"); err == nil || dirExists(codexHome()) {
			out = append(out, "codex")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("cli: unknown harness %q (want auto|claude|codex|all)", v)
	}
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func codexHome() string {
	if path := strings.TrimSpace(os.Getenv("CODEX_HOME")); path != "" {
		return filepath.Clean(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// registerMCP registers `culi mcp` as a user-scope MCP server via the claude
// CLI — the CLI owns ~/.claude.json, so we never edit that file ourselves
// (C4). Best-effort: a missing/odd claude binary just prints the manual step.
func registerMCPClaude() {
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

// registerMCPCodex lets the Codex CLI own its config.toml. An existing culi
// entry is preserved: overwriting user MCP configuration is never implicit.
func registerMCPCodex() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	manual := fmt.Sprintf("mcp:      run: codex mcp add culi -- %s mcp", quoteCommandArg(exe))
	codex, err := exec.LookPath("codex")
	if err != nil {
		fmt.Println(manual)
		return
	}
	if out, err := exec.Command(codex, "mcp", "get", "culi", "--json").CombinedOutput(); err == nil {
		if codexMCPMatches(out, exe) {
			fmt.Println("mcp:      codex already registered")
		} else {
			fmt.Printf("warning:  codex MCP entry named culi already exists and was preserved\n"+
				"mcp:      to replace it, run `codex mcp remove culi`, then: %s\n", strings.TrimPrefix(manual, "mcp:      run: "))
		}
		return
	}
	if out, err := exec.Command(codex, "mcp", "add", "culi", "--", exe, "mcp").CombinedOutput(); err != nil {
		fmt.Printf("warning:  codex mcp add failed (%v: %s)\n%s\n", err, strings.TrimSpace(string(out)), manual)
		return
	}
	fmt.Println("mcp:      codex registered — tools: search_context, expand_card, save_lesson")
}

func codexMCPMatches(raw []byte, exe string) bool {
	var cfg struct {
		Command   string   `json:"command"` // older CLI shape
		Args      []string `json:"args"`
		Transport struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"transport"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	if cfg.Transport.Command != "" {
		return cfg.Transport.Type == "stdio" && cfg.Transport.Command == exe &&
			len(cfg.Transport.Args) == 1 && cfg.Transport.Args[0] == "mcp"
	}
	return cfg.Command == exe && len(cfg.Args) == 1 && cfg.Args[0] == "mcp"
}

// registerHooks merges culi's three hook entries into ~/.claude/settings.json,
// preserving everything else. Never destroys user content (C4): the original
// is backed up once before the first modification.
func registerHooksClaude() (changed bool, err error) {
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

	// Statusline: register only when the user has none — an existing
	// statusLine is user content (C4); they append `culi statusline` to their
	// own script instead.
	if _, exists := settings["statusLine"]; !exists {
		settings["statusLine"] = map[string]any{"type": "command", "command": exe + " statusline"}
		changed = true
		fmt.Println("status:   statusline registered (live card/token/review segment)")
	} else if sl, _ := settings["statusLine"].(map[string]any); sl != nil {
		if cmd, _ := sl["command"].(string); !strings.Contains(cmd, "culi") {
			fmt.Println("status:   you already have a statusLine — append `culi statusline` output to it for the culi segment")
		}
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

// registerHooksCodex merges culi's Codex hooks into $CODEX_HOME/hooks.json.
// The top-level "hooks" object is required by Codex's lifecycle schema.
func registerHooksCodex() (changed bool, err error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("cli: resolving executable: %w", err)
	}
	dir := codexHome()
	if dir == "" {
		return false, fmt.Errorf("cli: resolving CODEX_HOME")
	}
	path := filepath.Join(dir, "hooks.json")
	root := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("cli: reading Codex hooks.json: %w", err)
	default:
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil {
				return false, fmt.Errorf("cli: Codex hooks.json is not valid JSON: %w", err)
			}
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range []struct {
		name, event string
		timeout     float64
	}{
		{"SessionStart", "session-start", 10},
		{"UserPromptSubmit", "user-prompt-submit", 10},
		{"Stop", "stop", 30},
		{"SessionEnd", "session-end", 30},
	} {
		if hasCuliHook(hooks[ev.name]) {
			continue
		}
		cmd := joinCommand(exe, "hook", ev.event, "--harness=codex")
		entry := map[string]any{"type": "command", "command": cmd, "timeout": ev.timeout}
		group := map[string]any{"hooks": []any{entry}}
		list, _ := hooks[ev.name].([]any)
		hooks[ev.name] = append(list, group)
		changed = true
	}
	if !changed {
		return false, nil
	}
	root["hooks"] = hooks
	if raw != nil {
		backup := path + ".culi-backup"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backup, raw, 0o644); err != nil {
				return false, fmt.Errorf("cli: writing Codex hooks backup: %w", err)
			}
		}
	}
	if err := atomicWriteJSON(path, root, ".hooks-culi-*"); err != nil {
		return false, err
	}
	fmt.Println("hooks:    Codex requires review — open a new session and run `/hooks` to trust culi")
	return true, nil
}

func atomicWriteJSON(path string, v any, pattern string) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("cli: marshaling %s: %w", filepath.Base(path), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cli: creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return fmt.Errorf("cli: creating temporary %s: %w", filepath.Base(path), err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("cli: writing temporary %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("cli: closing temporary %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("cli: replacing %s: %w", filepath.Base(path), err)
	}
	return nil
}

func joinCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteCommandArg(arg)
	}
	return strings.Join(quoted, " ")
}

func quoteCommandArg(s string) string {
	if runtime.GOOS == "windows" {
		return strconv.Quote(s)
	}
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
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
			if filepath.Base(firstField(cmd)) == "culi" ||
				(strings.Contains(cmd, " hook ") && strings.Contains(cmd, "--harness=codex")) {
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
