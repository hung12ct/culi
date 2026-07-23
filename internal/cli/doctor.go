package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/learn/codexscan"
)

// Doctor reports locally verifiable harness wiring. Codex does not expose its
// hook trust decisions through config, so doctor states that limitation rather
// than presenting "configured" as "trusted".
func Doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	harnessFlag := fs.String("harness", harness.Codex.String(), "harness to inspect: codex")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	if *harnessFlag != harness.Codex.String() {
		return fmt.Errorf("cli: doctor currently supports --harness=codex")
	}
	return doctorCodex(os.Stdout)
}

func doctorCodex(w io.Writer) error {
	home := codexHome()
	configured, timeoutOK := codexHookStatus(filepath.Join(home, "hooks.json"))
	fmt.Fprintf(w, "Codex home  %s\n", home)
	if configured == 4 {
		fmt.Fprintln(w, "Hooks       4/4 configured (trust: verify with Codex `/hooks`)")
	} else {
		fmt.Fprintf(w, "Hooks       %d/4 configured (run `culi init --harness=codex`)\n", configured)
	}
	if configured > 0 && !timeoutOK {
		fmt.Fprintln(w, "Timeouts    SessionEnd needs repair (run `culi init --harness=codex`)")
	} else if configured > 0 {
		fmt.Fprintln(w, "Timeouts    aligned with Codex SessionEnd 3s ceiling")
	}

	if codexMCPConfigured(filepath.Join(home, "config.toml")) {
		fmt.Fprintln(w, "MCP         culi configured")
	} else {
		fmt.Fprintln(w, "MCP         culi not detected (run `culi init --harness=codex`)")
	}

	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	if line := lastCodexHook(filepath.Join(config.LogDir(base), "hook.log")); line != "" {
		fmt.Fprintf(w, "Last hook   %s\n", line)
	} else {
		fmt.Fprintln(w, "Last hook   none observed; start a new Codex session and submit a prompt")
	}

	scanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	sessions, skipped, scanErr := codexscan.Discover(scanCtx, home)
	cancel()
	if scanErr != nil {
		fmt.Fprintf(w, "Scanner     unavailable: %s\n", safeScanError(scanErr, base, home))
	} else {
		fmt.Fprintf(w, "Scanner     ready (%d rollout(s) discoverable, %d skipped)\n", len(sessions), skipped)
	}
	health, healthErr := codexscan.LoadHealth(config.StateDir(base))
	fmt.Fprintf(w, "Last scan   %s\n", formatCodexScanHealth(health, healthErr))

	pending := pendingCodexJobs(config.InboxDir(base))
	fmt.Fprintf(w, "Learning    %d Codex transcript job(s) pending\n", pending)
	return nil
}

func formatCodexScanHealth(h codexscan.Health, err error) string {
	if err != nil {
		return "health unreadable: " + err.Error()
	}
	if h.LastAttempt.IsZero() {
		return "never (automatic fallback runs from Codex lifecycle hooks)"
	}
	stamp := h.LastAttempt.UTC().Format(time.RFC3339)
	mode := h.Mode
	if mode == "" {
		mode = "unknown"
	}
	if h.Error != "" {
		lastOK := ""
		if !h.LastSuccess.IsZero() {
			lastOK = "; last success " + h.LastSuccess.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf("%s %s failed: %s%s", stamp, mode, h.Error, lastOK)
	}
	return fmt.Sprintf("%s %s ok (%d discovered, %d queued, %d skipped, %dms)",
		stamp, mode, h.Discovered, h.Queued, h.Skipped, h.DurationMS)
}

// pendingCodexJobs is deliberately read-only. A doctor command must not park
// malformed queue entries or otherwise repair state while it is diagnosing it.
func pendingCodexJobs(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var job struct {
			Source         harness.Harness `json:"source"`
			TranscriptPath string          `json:"transcript_path"`
		}
		if json.Unmarshal(raw, &job) == nil && job.Source == harness.Codex && job.TranscriptPath != "" {
			n++
		}
	}
	return n
}

func codexHookStatus(path string) (configured int, sessionEndTimeoutOK bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return 0, false
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		if hasCuliHook(hooks[event]) {
			configured++
		}
	}
	return configured, culiHookTimeout(hooks["SessionEnd"]) == 3
}

func culiHookTimeout(v any) float64 {
	groups, _ := v.([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, " hook ") && strings.Contains(cmd, "--harness=codex") {
				timeout, _ := hm["timeout"].(float64)
				return timeout
			}
		}
	}
	return 0
}

func codexMCPConfigured(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	inCuli := false
	for rawLine := range strings.SplitSeq(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") {
			// Tolerate a trailing comment on the header, e.g.
			// `[mcp_servers.culi]  # added by culi init`.
			header := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			inCuli = header == "[mcp_servers.culi]"
			continue
		}
		// Inline-table form: `mcp_servers.culi = { command = "…culi", … }`.
		if strings.HasPrefix(line, "mcp_servers.culi") && strings.Contains(line, "command") && strings.Contains(line, "culi") {
			return true
		}
		if inCuli && strings.HasPrefix(line, "command") && strings.Contains(line, "culi") {
			return true
		}
	}
	return false
}

func lastCodexHook(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "session="+harness.Codex.String()+":") {
			last = line
		}
	}
	return last
}
