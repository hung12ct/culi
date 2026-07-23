package cli

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/learn/codexscan"
	"github.com/hung12ct/culi/internal/learn/queue"
	_ "modernc.org/sqlite"
)

func TestEnqueueCodexSessionsSkipsPendingAndMined(t *testing.T) {
	base := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte("one useful conversation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := []codexscan.Session{{
		SessionID: "abc", RolloutPath: rollout, CWD: "/repo", UpdatedAt: time.Now().UTC(),
	}}
	queued, err := enqueueCodexSessions(base, sessions)
	if err != nil || queued != 1 {
		t.Fatalf("first enqueue = %d, %v", queued, err)
	}
	queued, err = enqueueCodexSessions(base, sessions)
	if err != nil || queued != 0 {
		t.Fatalf("pending enqueue = %d, %v", queued, err)
	}
	jobs, err := queue.List(config.InboxDir(base))
	if err != nil || len(jobs) != 1 || jobs[0].SessionID != harness.Codex.PrefixSession("abc") {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	if err := queue.Done(jobs[0]); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(rollout)
	cursors := queue.LoadCursors(config.StateDir(base))
	cursors.Set(rollout, queue.Cursor{Offset: info.Size(), MinedAt: time.Now().UTC()})
	if err := cursors.Save(); err != nil {
		t.Fatal(err)
	}
	queued, err = enqueueCodexSessions(base, sessions)
	if err != nil || queued != 0 {
		t.Fatalf("mined enqueue = %d, %v", queued, err)
	}
}

func TestLearnAutomaticCodexScanThrottlesAndFinalForces(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte("learn:\n  enabled: true\n  provider: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(sessionsDir, "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(codexHome, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT, rollout_path TEXT, cwd TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threads VALUES ('abc', ?, '/repo', 100)`, rollout); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CULI_HOME", base)
	t.Setenv("CODEX_HOME", codexHome)

	if err := Learn([]string{"--auto", "--scan-codex"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.List(config.InboxDir(base))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first scan jobs = %+v, %v", jobs, err)
	}
	if err := queue.Done(jobs[0]); err != nil {
		t.Fatal(err)
	}
	if err := Learn([]string{"--auto", "--scan-codex"}); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 0 {
		t.Fatalf("throttled scan recreated jobs: %+v", jobs)
	}
	if err := Learn([]string{"--auto", "--scan-codex", "--scan-codex-force"}); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := queue.List(config.InboxDir(base)); len(jobs) != 1 {
		t.Fatalf("forced final scan jobs = %+v", jobs)
	}
	h, err := codexscan.LoadHealth(config.StateDir(base))
	if err != nil || h.Mode != "auto" || h.Discovered != 1 || h.Queued != 1 {
		t.Fatalf("health = %+v, %v", h, err)
	}
}

func TestRecordCodexScanAndSafeError(t *testing.T) {
	base := t.TempDir()
	started := time.Now().UTC().Add(-20 * time.Millisecond)
	if err := recordCodexScan(base, started, true, 5, 2, 1, nil); err != nil {
		t.Fatal(err)
	}
	h, err := codexscan.LoadHealth(config.StateDir(base))
	if err != nil || h.Mode != "auto" || h.Discovered != 5 || h.Queued != 2 || h.Error != "" || h.LastSuccess.IsZero() {
		t.Fatalf("health = %+v, %v", h, err)
	}
	err = errors.New("reading " + base + " and /tmp/codex-home/state.sqlite")
	got := safeScanError(err, base, "/tmp/codex-home")
	if !strings.Contains(got, "$CULI_HOME") || !strings.Contains(got, "$CODEX_HOME") {
		t.Fatalf("safe error = %q", got)
	}
}
