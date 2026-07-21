package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/learn/queue"
)

// stubClaude fakes the claude CLI: every invocation returns the same wrapper
// JSON whose result is a mining payload. Exercises the full worker path —
// queue → throttle → transcript → claude-cli backend → cards — offline.
func stubClaude(t *testing.T, innerJSON string) {
	t.Helper()
	dir := t.TempDir()
	wrapper, err := json.Marshal(map[string]any{
		"type": "result", "is_error": false, "result": innerJSON,
		"usage": map[string]int{"input_tokens": 100, "output_tokens": 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat > /dev/null\ncat <<'EOF'\n" + string(wrapper) + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const minePayload = `{"lessons":[{"slug":"wrap-errors","title":"Wrap errors with package prefix","summary":"Wrap, never panic.","markdown":"Wrap errors as fmt.Errorf with package prefix.","keywords":["errors"],"scope":"lang:go","confidence":0.9,"evidence":"no dont panic","supersedes":""}],"missing_rules":[],"style_observations":[],"notes":""}`

// setupBase creates a sandbox culi home with one queued correction session.
func setupBase(t *testing.T) (string, config.Config) {
	t.Helper()
	base := t.TempDir()
	for _, d := range []string{
		filepath.Join(config.KnowledgeDir(base), "lessons"),
		config.InboxDir(base),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	transcript := filepath.Join(base, "session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"write the client"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done with panics"}]}}`,
		`{"type":"user","message":{"role":"user","content":"No, wrong - wrap errors instead of panicking"}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := `{"session_id":"s1","transcript_path":"` + transcript + `","cwd":"` + base + `","enqueued_at":"2026-07-19T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(config.InboxDir(base), "job1.json"), []byte(job), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	return base, cfg
}

func TestRunEndToEndOverStubCLI(t *testing.T) {
	base, cfg := setupBase(t)
	stubClaude(t, minePayload)
	cfg.Learn.Provider = "claude-cli"
	cfg.Ollama.Endpoint = "http://127.0.0.1:1" // dead: dedup falls back to lexical

	sum, err := Run(context.Background(), base, cfg, Options{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Jobs != 1 || sum.Mined != 1 || len(sum.Created) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	// Parse-gated deletion: job gone, cursor persisted.
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 0 {
		t.Errorf("job not drained: %v", jobs)
	}
	cur := queue.LoadCursors(config.StateDir(base)).Get(filepath.Join(base, "session.jsonl"))
	if cur.Offset == 0 {
		t.Error("cursor not persisted")
	}
	// Card exists on disk under lessons/.
	if _, err := os.Stat(filepath.Join(config.KnowledgeDir(base), sum.Created[0]+".md")); err != nil {
		t.Errorf("card missing: %v", err)
	}
	// Re-run: nothing new → job-less no-op.
	sum2, err := Run(context.Background(), base, cfg, Options{}, t.Logf)
	if err != nil || sum2.Jobs != 0 {
		t.Errorf("second run = %+v, %v", sum2, err)
	}
}

// TestRunNoCapParallelDrain exercises the --no-cap parallel path (bounded pool,
// WriteMu-serialized store writes, concurrent ledger). Run under -race.
func TestRunNoCapParallelDrain(t *testing.T) {
	base, cfg := setupBase(t) // seeds job1
	stubClaude(t, minePayload)
	cfg.Learn.Provider = "claude-cli"
	cfg.Ollama.Endpoint = "http://127.0.0.1:1" // dead: lexical dedup

	seedSessions(t, base, 5) // + jobs 2..6 so the drain has jobs to fan out

	sum, err := Run(context.Background(), base, cfg, Options{IgnoreCaps: true}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Jobs != 6 {
		t.Fatalf("jobs = %d, want 6; summary = %+v", sum.Jobs, sum)
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 0 {
		t.Errorf("parallel drain left %d jobs queued", len(jobs))
	}
	if len(sum.Created) == 0 { // exact count is nondeterministic under concurrency
		t.Errorf("no cards created: %+v", sum)
	}
}

// seedSessions writes n extra minable transcripts + jobs into base.
func seedSessions(t *testing.T, base string, n int) {
	t.Helper()
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"write the client"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done with panics"}]}}`,
		`{"type":"user","message":{"role":"user","content":"No, wrong - wrap errors instead of panicking"}}`,
	}, "\n") + "\n"
	for i := 2; i <= n+1; i++ {
		tp := filepath.Join(base, fmt.Sprintf("session%d.jsonl", i))
		if err := os.WriteFile(tp, []byte(lines), 0o644); err != nil {
			t.Fatal(err)
		}
		job := fmt.Sprintf(`{"session_id":"s%d","transcript_path":%q,"cwd":%q,"enqueued_at":"2026-07-19T00:00:0%dZ"}`, i, tp, base, i)
		if err := os.WriteFile(filepath.Join(config.InboxDir(base), fmt.Sprintf("job%d.json", i)), []byte(job), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A logged-out backend during a --no-cap parallel drain must halt, keep every
// job QUEUED, and park nothing (an env problem, not bad transcripts).
func TestRunNoCapParallelHaltOnBackendDown(t *testing.T) {
	base, cfg := setupBase(t) // job1
	seedSessions(t, base, 5)  // + jobs 2..6
	dir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\necho 'Not logged in · Please run /login'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg.Learn.Provider = "claude-cli"
	cfg.Ollama.Endpoint = "http://127.0.0.1:1"

	sum, err := Run(context.Background(), base, cfg, Options{IgnoreCaps: true}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.BackendDown {
		t.Errorf("want BackendDown, got %+v", sum)
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 6 {
		t.Errorf("jobs not kept queued on backend-down: %d of 6 remain", len(jobs))
	}
	if failed, _ := os.ReadDir(filepath.Join(config.InboxDir(base), "failed")); len(failed) > 0 {
		t.Errorf("jobs parked on a backend problem: %d", len(failed))
	}
}

func TestRunDisabledKeepsQueue(t *testing.T) {
	base, cfg := setupBase(t)
	cfg.Learn.Enabled = false
	sum, err := Run(context.Background(), base, cfg, Options{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Notes) == 0 {
		t.Error("want a disabled note")
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 1 {
		t.Errorf("disabled run touched the queue: %v", jobs)
	}
}

func TestRunNoBackendKeepsQueue(t *testing.T) {
	base, cfg := setupBase(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("PATH", t.TempDir()) // no claude
	sum, err := Run(context.Background(), base, cfg, Options{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Notes) == 0 || !strings.Contains(sum.Notes[0], "backend") {
		t.Errorf("notes = %v", sum.Notes)
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 1 {
		t.Errorf("backend-less run touched the queue: %v", jobs)
	}
}

func TestRunFailLadderOnGarbageBackend(t *testing.T) {
	base, cfg := setupBase(t)
	stubClaude(t, "utter garbage, no json")
	cfg.Learn.Provider = "claude-cli"

	sum, err := Run(context.Background(), base, cfg, Options{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Created) != 0 || len(sum.Notes) == 0 {
		t.Fatalf("summary = %+v", sum)
	}
	jobs, _ := queue.List(config.InboxDir(base))
	if len(jobs) != 1 || jobs[0].Attempts() != 1 {
		t.Errorf("fail ladder: %+v", jobs)
	}
}
