// Package transcript reads Claude Code session transcripts (JSONL) and
// extracts deterministic signal windows for the learning miner — the zero-LLM
// stage 0 that decides whether a session is worth any model call at all.
//
// The transcript format is undocumented and shifts across Claude Code
// versions, so all format knowledge is isolated here and every decode is
// permissive: unknown line types are skipped, a malformed line never fails the
// file (plan §learning risk).
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Entry is one normalized conversation turn.
type Entry struct {
	Role       string   // "user" | "assistant"
	Text       string   // concatenated text content (typed prompts, replies)
	ToolErrors []string // is_error tool_result payloads (rejections, failures)
}

// ReadFrom parses entries starting at byte offset and returns them with the
// new offset (end of the last complete line — a partial trailing line is left
// for the next run). An offset past EOF means the file was truncated or
// replaced: parsing restarts from 0.
func ReadFrom(path string, offset int64) ([]Entry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("transcript: opening %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, fmt.Errorf("transcript: stat %s: %w", path, err)
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("transcript: seeking %s: %w", path, err)
	}

	// Transcript lines routinely exceed bufio.Scanner's token limit (inlined
	// tool results, file snapshots), so read raw lines instead.
	r := bufio.NewReaderSize(f, 256<<10)
	var entries []Entry
	pos := offset
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return entries, pos, fmt.Errorf("transcript: reading %s: %w", path, err)
		}
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if complete {
			pos += int64(len(line))
			if e, ok := parseLine(line); ok {
				entries = append(entries, e)
			}
		}
		if err == io.EOF {
			return entries, pos, nil
		}
	}
}

// rawLine is the loose superset of transcript line shapes we consume.
// Everything else (mode, attachment, file-history-snapshot, ai-title, …) is
// skipped by type.
type rawLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// rawItem is one content-array element: text, thinking, tool_use, tool_result.
type rawItem struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"` // tool_result payload: string or [{type,text}]
}

// parseLine normalizes one JSONL line; ok=false for anything skippable.
// Sidechain (subagent) traffic is dropped — only the main session is mined.
func parseLine(line []byte) (Entry, bool) {
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return Entry{}, false
	}
	if (rl.Type != "user" && rl.Type != "assistant") || rl.IsSidechain {
		return Entry{}, false
	}
	e := Entry{Role: rl.Type}
	if rl.Message.Role != "" {
		e.Role = rl.Message.Role
	}

	// Content is either a plain string (typed user prompts) or an item array.
	var s string
	if err := json.Unmarshal(rl.Message.Content, &s); err == nil {
		e.Text = s
		return e, e.Text != ""
	}
	var items []rawItem
	if err := json.Unmarshal(rl.Message.Content, &items); err != nil {
		return Entry{}, false
	}
	for _, it := range items {
		switch it.Type {
		case "text":
			if it.Text != "" {
				if e.Text != "" {
					e.Text += "\n"
				}
				e.Text += it.Text
			}
		case "tool_result":
			if it.IsError {
				if t := itemPayloadText(it.Content); t != "" {
					e.ToolErrors = append(e.ToolErrors, t)
				}
			}
		}
	}
	return e, e.Text != "" || len(e.ToolErrors) > 0
}

// itemPayloadText extracts text from a tool_result payload, which is either a
// bare string or a nested [{type:"text", text}] array.
func itemPayloadText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var items []rawItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	out := ""
	for _, it := range items {
		if it.Type == "text" && it.Text != "" {
			if out != "" {
				out += "\n"
			}
			out += it.Text
		}
	}
	return out
}
