package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// line builds one transcript JSONL line in the real Claude Code shape.
func line(t *testing.T, typ string, content any, sidechain bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":        typ,
		"isSidechain": sidechain,
		"message":     map[string]any{"role": typ, "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func userText(t *testing.T, s string) string { return line(t, "user", s, false) }

func assistantText(t *testing.T, s string) string {
	return line(t, "assistant", []map[string]any{{"type": "text", "text": s}}, false)
}

func toolError(t *testing.T, msg string) string {
	return line(t, "user", []map[string]any{
		{"type": "tool_result", "is_error": true, "content": msg},
	}, false)
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFromShapesAndCursor(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"mode","mode":"default"}`, // skipped line type
		userText(t, "please add retries"),
		assistantText(t, "done"),
		line(t, "user", "sidechain noise", true), // subagent traffic dropped
		"not json at all",                        // malformed: skipped, not fatal
		toolError(t, "exit status 1"),
	)
	entries, off, err := ReadFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	if entries[0].Role != "user" || entries[0].Text != "please add retries" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if len(entries[2].ToolErrors) != 1 || entries[2].ToolErrors[0] != "exit status 1" {
		t.Errorf("entry 2 tool errors = %+v", entries[2].ToolErrors)
	}
	st, _ := os.Stat(path)
	if off != st.Size() {
		t.Errorf("offset = %d, want file size %d", off, st.Size())
	}

	// Resume from the returned offset: nothing new.
	more, off2, err := ReadFrom(path, off)
	if err != nil || len(more) != 0 || off2 != off {
		t.Errorf("resume = %d entries, off %d→%d, err %v", len(more), off, off2, err)
	}

	// Append one more line; only it is returned.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(userText(t, "new prompt") + "\n")
	f.Close()
	more, _, err = ReadFrom(path, off)
	if err != nil || len(more) != 1 || more[0].Text != "new prompt" {
		t.Errorf("appended read = %+v, err %v", more, err)
	}

	// Offset past EOF (rotated file) restarts from 0.
	all, _, err := ReadFrom(path, 1<<40)
	if err != nil || len(all) != 4 {
		t.Errorf("rotated read = %d entries, err %v", len(all), err)
	}
}

func TestReadFromPartialTrailingLine(t *testing.T) {
	path := writeTranscript(t, userText(t, "hello world question"))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"user","message":`) // torn write, no newline
	f.Close()
	entries, off, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err %v", len(entries), err)
	}
	st, _ := os.Stat(path)
	if off >= st.Size() {
		t.Errorf("offset %d should stop before the torn line (size %d)", off, st.Size())
	}
}

func TestNestedToolResultPayload(t *testing.T) {
	path := writeTranscript(t, line(t, "user", []map[string]any{
		{"type": "tool_result", "is_error": true,
			"content": []map[string]any{{"type": "text", "text": "lint failed"}}},
	}, false))
	entries, _, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 1 || entries[0].ToolErrors[0] != "lint failed" {
		t.Fatalf("entries = %+v, err %v", entries, err)
	}
}

func TestExtractCorrectionAndRejection(t *testing.T) {
	entries := []Entry{
		{Role: "user", Text: "please write the parser"},
		{Role: "assistant", Text: "done, I used panics for errors"},
		{Role: "user", Text: "No, don't panic — wrap errors instead"},
		{Role: "assistant", Text: "fixed"},
		{Role: "user", Text: "sai rồi, không phải như vậy"},
		{Role: "user", ToolErrors: []string{"The user doesn't want to proceed with this tool use. The user said: use the Makefile"}},
	}
	wins := Extract(entries)
	if len(wins) == 0 {
		t.Fatal("no windows extracted")
	}
	// All triggers overlap into few windows; highest-priority reason wins.
	found := map[string]bool{}
	for _, w := range wins {
		found[w.Reason] = true
	}
	if !found["rejection"] && !found["correction"] {
		t.Errorf("windows = %+v, want a correction/rejection window", wins)
	}
}

func TestExtractRepeatedInstruction(t *testing.T) {
	instr := "always run gofmt and golangci lint before committing changes"
	entries := []Entry{
		{Role: "user", Text: instr},
		{Role: "assistant", Text: "ok"},
		{Role: "assistant", Text: "more work"},
		{Role: "assistant", Text: "more work"},
		{Role: "user", Text: "remember: " + instr},
	}
	wins := Extract(entries)
	if len(wins) != 1 || wins[0].Reason != "repeated" {
		t.Fatalf("windows = %+v, want one repeated window", wins)
	}
}

func TestExtractCleanSessionHasNoWindows(t *testing.T) {
	entries := []Entry{
		{Role: "user", Text: "please add a retry helper to the fetch client"},
		{Role: "assistant", Text: "added with backoff"},
		{Role: "user", Text: "great, now add tests for it please"},
		{Role: "assistant", Text: "tests added and passing"},
	}
	if wins := Extract(entries); len(wins) != 0 {
		t.Errorf("clean session produced windows: %+v", wins)
	}
}

func TestCapDropsToolErrorsFirst(t *testing.T) {
	var entries []Entry
	for range 40 {
		entries = append(entries,
			Entry{Role: "assistant", Text: "trying"},
			Entry{Role: "user", ToolErrors: []string{"exit status 1"}},
			Entry{Role: "assistant", Text: "retrying"},
			Entry{Role: "assistant", Text: "ok"},
			Entry{Role: "assistant", Text: "next"},
			Entry{Role: "assistant", Text: "step"},
		)
	}
	entries = append(entries, Entry{Role: "user", Text: "wrong, use the other approach"})
	wins := Extract(entries)
	if len(wins) > maxWindows {
		t.Fatalf("cap failed: %d windows", len(wins))
	}
	hasCorrection := false
	for _, w := range wins {
		if w.Reason == "correction" {
			hasCorrection = true
		}
	}
	if !hasCorrection {
		t.Error("correction window was dropped before tool_error windows")
	}
}

func TestRenderCapsBytes(t *testing.T) {
	entries := []Entry{
		{Role: "user", Text: strings.Repeat("x", 5000)},
		{Role: "user", Text: "no, wrong"},
		{Role: "user", Text: "no, still wrong"},
	}
	wins := Extract(entries)
	out := Render(entries, wins, 800)
	if len(out) > 1200 { // window cap + truncation notice headroom
		t.Errorf("render = %d bytes, want ≤ ~800", len(out))
	}
	if !strings.Contains(out, "…") && !strings.Contains(out, "truncated") {
		t.Errorf("render should mark truncation:\n%s", out)
	}
}

func TestIsCorrectionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"No, use table tests", true},
		{"don't do that", true},
		{"DON'T ever push directly", true},
		{"không đúng, sửa lại đi", true},
		{"the word nothing contains no", false},
		{"a normal request to add a feature", false},
		{"casino night", false}, // "no" only counts message-initial
	} {
		if got := isCorrection(tc.text); got != tc.want {
			t.Errorf("isCorrection(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
