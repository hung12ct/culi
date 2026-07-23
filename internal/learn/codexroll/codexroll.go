// Package codexroll normalizes Codex rollout JSONL into the conversation
// entries consumed by the learning miner. Rollouts are intentionally treated
// as a permissive input: unknown records are skipped because the format is not
// a stable public API.
package codexroll

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hung12ct/culi/internal/learn/transcript"
)

type line struct {
	Type    string  `json:"type"`
	Payload payload `json:"payload"`
}

type payload struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content []contentItem   `json:"content"`
	Output  json.RawMessage `json:"output"`
	IsError bool            `json:"is_error"`
	Status  string          `json:"status"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ReadFrom parses complete records beginning at offset. An offset beyond EOF
// resets to zero, matching the Claude transcript reader's cursor contract.
func ReadFrom(path string, offset int64) ([]transcript.Entry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("codexroll: opening %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, fmt.Errorf("codexroll: stat %s: %w", path, err)
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("codexroll: seeking %s: %w", path, err)
	}

	r := bufio.NewReaderSize(f, 256<<10)
	entries := []transcript.Entry{}
	pos := offset
	for {
		raw, err := r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return entries, pos, fmt.Errorf("codexroll: reading %s: %w", path, err)
		}
		complete := len(raw) > 0 && raw[len(raw)-1] == '\n'
		if complete {
			pos += int64(len(raw))
			if e, ok := parseLine(raw); ok {
				entries = append(entries, e)
			}
		}
		if err == io.EOF {
			return entries, pos, nil
		}
	}
}

func parseLine(raw []byte) (transcript.Entry, bool) {
	var rl line
	if json.Unmarshal(raw, &rl) != nil || rl.Type != "response_item" {
		return transcript.Entry{}, false
	}
	if rl.Payload.Type == "message" && (rl.Payload.Role == "user" || rl.Payload.Role == "assistant") {
		var texts []string
		for _, item := range rl.Payload.Content {
			if (item.Type == "input_text" || item.Type == "output_text") && item.Text != "" {
				texts = append(texts, item.Text)
			}
		}
		if len(texts) == 0 {
			return transcript.Entry{}, false
		}
		text := strings.Join(texts, "\n")
		// Codex injects synthetic "user" messages (sandbox/cwd state, the
		// plugin catalog, …) that are harness state, not user intent. Mining
		// them would teach Culi environment metadata as if it were a reusable
		// lesson, so drop any user turn that opens with a known envelope tag.
		if rl.Payload.Role == "user" && isHarnessEnvelope(text) {
			return transcript.Entry{}, false
		}
		return transcript.Entry{Role: rl.Payload.Role, Text: text}, true
	}

	// Tool output is mined only when the record explicitly marks failure.
	// Never guess from arbitrary command output containing words like "error".
	if strings.HasSuffix(rl.Payload.Type, "tool_call_output") &&
		(rl.Payload.IsError || rl.Payload.Status == "error" || rl.Payload.Status == "failed") {
		if text := outputText(rl.Payload.Output); text != "" {
			return transcript.Entry{Role: "user", ToolErrors: []string{text}}, true
		}
	}
	return transcript.Entry{}, false
}

// codexHarnessEnvelopes are the opening tags of Codex-injected synthetic
// "user" messages observed in real rollouts (Codex v0.145). A prefix match is
// deliberate: Codex may append trailing text after the block, and a real user
// prompt is extraordinarily unlikely to open with one of these exact tags.
// Extend this list as new envelopes surface in the wild.
var codexHarnessEnvelopes = []string{
	"<environment_context>",
	"<recommended_plugins>",
}

func isHarnessEnvelope(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, tag := range codexHarnessEnvelopes {
		if strings.HasPrefix(trimmed, tag) {
			return true
		}
	}
	return false
}

func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []contentItem
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var texts []string
	for _, item := range items {
		if (item.Type == "input_text" || item.Type == "output_text" || item.Type == "text") && item.Text != "" {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}
