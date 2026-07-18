// Package hook implements the Claude Code hook handlers. This is the hot
// path: it runs on every prompt the user types.
//
// The two contracts that own this package (see /project-rule):
//   - C1 fail-open (14.40): any error ⇒ log + empty stdout + exit 0. stdout
//     belongs to the hook JSON protocol; the ONLY stdout write is the single
//     success emit in Run.
//   - C2 deadline (14.41): all work runs under a context deadline below the
//     settings.json hook timeout; a fired deadline logs a breadcrumb so
//     silent degradation stays observable.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/hung12ct/culi/internal/config"
)

// Input is the normalized hook event. Claude Code's stdin JSON maps directly;
// other harnesses become adapters onto this struct (ecosystem learning:
// normalized event seam from day one).
type Input struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Prompt         string `json:"prompt"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"` // SessionStart: startup|resume|clear|compact
}

// output is the Claude Code hook stdout contract.
type output struct {
	SuppressOutput     bool          `json:"suppressOutput"`
	HookSpecificOutput *specificShim `json:"hookSpecificOutput,omitempty"`
}

type specificShim struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// Per-event internal deadlines — all below the settings.json timeouts.
const (
	promptDeadline  = 150 * time.Millisecond
	sessionDeadline = 5 * time.Second
	maxOutputChars  = 10000 // Claude Code truncates additionalContext at 10k
)

// Run executes one hook event, reading stdin JSON from in and writing the
// protocol response to out. ALWAYS returns 0.
func Run(args []string, in io.Reader, out io.Writer) (code int) {
	base, err := config.BaseDir()
	if err != nil {
		// Nowhere to log; stderr is protocol-safe and aids debugging.
		fmt.Fprintf(os.Stderr, "culi hook: resolving base dir: %v\n", err)
		return 0
	}
	defer func() {
		if r := recover(); r != nil {
			logf(base, "panic: %v", r)
			code = 0
		}
	}()
	if len(args) < 1 {
		logf(base, "hook: missing event argument")
		return 0
	}
	event := args[0]

	deadline := promptDeadline
	eventName := "UserPromptSubmit"
	switch event {
	case "user-prompt-submit":
	case "session-start":
		deadline, eventName = sessionDeadline, "SessionStart"
	case "session-end":
		deadline, eventName = sessionDeadline, ""
	default:
		logf(base, "hook: unknown event %q", event)
		return 0
	}

	var input Input
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&input); err != nil {
		logf(base, "hook %s: decoding stdin: %v", event, err)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	additional, err := handle(ctx, base, event, input)
	if err != nil {
		if ctx.Err() != nil {
			// Breadcrumb: a fired deadline must be observable (14.41 / #858).
			logf(base, "hook %s: DEADLINE after %s: %v", event, deadline, err)
		} else {
			logf(base, "hook %s: %v", event, err)
		}
		return 0
	}
	if additional == "" || eventName == "" {
		return 0 // nothing to inject: clean empty stdout
	}
	if len(additional) > maxOutputChars {
		// Final guard (C3); back up to a rune boundary so the tail is clean.
		cut := maxOutputChars
		for cut > 0 && !utf8.RuneStart(additional[cut]) {
			cut--
		}
		additional = additional[:cut]
	}
	resp := output{SuppressOutput: true, HookSpecificOutput: &specificShim{
		HookEventName: eventName, AdditionalContext: additional,
	}}
	raw, err := json.Marshal(resp)
	if err != nil {
		logf(base, "hook %s: marshaling output: %v", event, err)
		return 0
	}
	// The single stdout write in the entire hook path (14.40).
	if _, err := out.Write(raw); err != nil {
		logf(base, "hook %s: writing stdout: %v", event, err)
	}
	return 0
}

// logf appends to ~/.culi/logs/hook.log. Diagnostics never touch stdout.
func logf(base, format string, args ...any) {
	dir := config.LogDir(base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}
