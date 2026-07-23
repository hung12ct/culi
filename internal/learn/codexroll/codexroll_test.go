package codexroll

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRollout(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFromMessagesAndNoise(t *testing.T) {
	path := writeRollout(t,
		`{"type":"event_msg","payload":{"type":"user_message","message":"duplicate"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"secret instruction"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"please add retries"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	)
	entries, off, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%+v off=%d err=%v", entries, off, err)
	}
	if entries[0].Role != "user" || entries[0].Text != "please add retries" {
		t.Errorf("user entry = %+v", entries[0])
	}
	if entries[1].Role != "assistant" || entries[1].Text != "done" {
		t.Errorf("assistant entry = %+v", entries[1])
	}
}

func TestSkipsSyntheticEnvironmentContext(t *testing.T) {
	path := writeRollout(t,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n<cwd>/secret/repo</cwd>\n</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the retry bug"}]}}`,
	)
	entries, _, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 1 || entries[0].Text != "fix the retry bug" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestExplicitToolFailureOnly(t *testing.T) {
	path := writeRollout(t,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","output":"error appears in successful output"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","status":"failed","output":[{"type":"input_text","text":"permission denied"}]}}`,
	)
	entries, _, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 1 || len(entries[0].ToolErrors) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if entries[0].ToolErrors[0] != "permission denied" {
		t.Errorf("tool error = %q", entries[0].ToolErrors[0])
	}
}

func TestCursorAndPartialLine(t *testing.T) {
	path := writeRollout(t, `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}`)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"response_item"`)
	f.Close()
	entries, off, err := ReadFrom(path, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	st, _ := os.Stat(path)
	if off >= st.Size() {
		t.Errorf("offset %d consumed partial line (size %d)", off, st.Size())
	}
	entries, _, err = ReadFrom(path, 1<<40)
	if err != nil || len(entries) != 1 {
		t.Fatalf("reset entries=%+v err=%v", entries, err)
	}
}
