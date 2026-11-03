package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/store"
)

// doctorAll is the default `culi doctor`: one screen that says whether culi is
// working and, for every line that is not, the command that fixes it.
func doctorAll(w io.Writer) error {
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Fprintf(w, "culi        %s (%s)\n", versionLine(), exe)
	}
	doctorStore(w, base)
	fmt.Fprintf(w, "Ollama      %s\n", ollamaStatus(cfg.Ollama.Endpoint, cfg.Ollama.Model))
	if _, desc, err := llmtier.Resolve(cfg.Learn, cfg.Ollama.Endpoint, config.StateDir(base)); err != nil {
		fmt.Fprintf(w, "Learning    unavailable: %v\n            fix: `claude auth login` or `codex login`, or set learn.provider in config.yaml\n", err)
	} else {
		fmt.Fprintf(w, "Learning    %s\n", desc)
	}
	doctorFailed(w, base)

	home, _ := os.UserHomeDir()
	if dirExists(filepath.Join(home, ".claude")) {
		fmt.Fprintln(w)
		doctorClaude(w, home, base)
	}
	if dir := codexHome(); dir != "" && dirExists(dir) {
		fmt.Fprintln(w)
		return doctorCodex(w)
	}
	return nil
}

func versionLine() string {
	if version != "" {
		return version
	}
	return "dev"
}

func doctorStore(w io.Writer, base string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		fmt.Fprintf(w, "Store       unreadable: %v\n            fix: culi init\n", err)
		return
	}
	defer s.Close()
	metas, err := s.AllCardsMeta(ctx)
	if err != nil {
		fmt.Fprintf(w, "Store       unreadable: %v\n            fix: culi index --full\n", err)
		return
	}
	live, review := 0, 0
	for _, m := range metas {
		switch m.Status {
		case "candidate":
			review++
		case "retired":
		default:
			live++
		}
	}
	switch {
	case live == 0:
		fmt.Fprintf(w, "Store       empty, so nothing can be injected yet\n            fix: culi import scan, then culi import merge (turns CLAUDE.md, skills and agents into cards)\n")
	case review > 0:
		fmt.Fprintf(w, "Store       %d live cards, %d learned card(s) waiting for review (culi review or culi serve)\n", live, review)
	default:
		fmt.Fprintf(w, "Store       %d live cards\n", live)
	}
}

func doctorFailed(w io.Writer, base string) {
	parked, err := queue.Failed(config.InboxDir(base))
	if err != nil || len(parked) == 0 {
		return
	}
	fmt.Fprintf(w, "Failed jobs %d parked\n            fix: culi learn --failed (reasons), then --retry-failed or --clear-failed\n", len(parked))
}

// ollamaStatus reports whether semantic search is available. Ollama is
// optional, so every state is phrased as what the user gets, not as an error.
func ollamaStatus(endpoint, model string) string {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(endpoint, "/") + "/api/tags")
	if err != nil {
		if _, lookErr := exec.LookPath("ollama"); lookErr != nil {
			return "not installed; keyword search only (optional: install Ollama for semantic search)"
		}
		return "installed but not running; keyword search only\n            fix: ollama serve"
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return "running, but /api/tags was unreadable"
	}
	for _, m := range tags.Models {
		if m.Name == model || strings.TrimSuffix(m.Name, ":latest") == model {
			return model + " ready (semantic search on)"
		}
	}
	return fmt.Sprintf("running, but %s is not pulled; keyword search only\n            fix: ollama pull %s", model, model)
}

// doctorClaude checks the three Claude Code hooks, the statusline and the MCP
// registration, then shows the last prompt culi saw from Claude Code.
func doctorClaude(w io.Writer, home, base string) {
	settings := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	configured := 0
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
		if hasCuliHook(hooks[ev]) {
			configured++
		}
	}
	fmt.Fprintln(w, "Claude Code")
	if configured == 3 {
		fmt.Fprintln(w, "Hooks       3/3 configured")
	} else {
		fmt.Fprintf(w, "Hooks       %d/3 configured\n            fix: culi init --harness=claude\n", configured)
	}
	if sl, _ := settings["statusLine"].(map[string]any); sl != nil {
		if cmd, _ := sl["command"].(string); isCuliCommand(cmd) {
			fmt.Fprintln(w, "Statusline  culi")
		}
	}
	fmt.Fprintf(w, "MCP         %s\n", claudeMCPStatus())
	if line := lastHook(filepath.Join(config.LogDir(base), "hook.log"), harness.Claude); line != "" {
		fmt.Fprintf(w, "Last hook   %s\n", line)
	} else {
		fmt.Fprintln(w, "Last hook   none yet; open a new Claude Code session and send a prompt")
	}
}

func claudeMCPStatus() string {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "claude CLI not on PATH, cannot check"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, claude, "mcp", "get", "culi").Run() != nil {
		return "culi not registered\n            fix: culi init --harness=claude"
	}
	return "culi registered"
}

// lastHook returns the newest hook.log line for one harness.
func lastHook(path string, h harness.Harness) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	marker := "session=" + h.String() + ":"
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if strings.Contains(line, marker) {
			last = strings.TrimSpace(line)
		}
		if err != nil {
			return last
		}
	}
}
