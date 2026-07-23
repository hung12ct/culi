package codexscan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDiscoverReadOnlyDedupAndPathBoundary(t *testing.T) {
	home := t.TempDir()
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(sessionsDir, "rollout-one.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, version := range []string{"4", "5"} {
		path := filepath.Join(home, "state_"+version+".sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE threads (
			id TEXT, rollout_path TEXT, cwd TEXT, updated_at INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
			"one", rollout, "/repo", 100, "outside", outside, "/repo", 200); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}

	got, skipped, err := Discover(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	realRollout, _ := filepath.EvalSymlinks(rollout)
	if len(got) != 1 || got[0].SessionID != "one" || got[0].RolloutPath != realRollout {
		t.Fatalf("sessions=%+v", got)
	}
	if !got[0].UpdatedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("updated=%s", got[0].UpdatedAt)
	}
	// The out-of-home rollout must be observable as a drop, not silently
	// vanish. (Both state DBs carry the row, so the count is a per-DB sum.)
	if skipped == 0 {
		t.Fatal("out-of-home rollout was dropped without being counted")
	}
}

func TestDiscoverNeedsStateDatabase(t *testing.T) {
	if _, _, err := Discover(context.Background(), t.TempDir()); err == nil {
		t.Fatal("missing state database accepted")
	}
}
