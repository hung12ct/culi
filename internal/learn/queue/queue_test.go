package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeJob(t *testing.T, dir, name, transcript string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	raw := `{"session_id":"s1","transcript_path":"` + transcript + `","cwd":"/tmp","enqueued_at":"2026-07-19T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListDoneFailLadder(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "aaaa.json", "/tmp/a.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs, err := List(dir)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %v, err %v", jobs, err)
	}
	j := jobs[0]
	if j.Attempts() != 0 || j.SessionID != "s1" {
		t.Fatalf("job = %+v", j)
	}

	// Fail twice: renamed with .f1 then .f2, attempts parsed back on List.
	for want := 1; want <= 2; want++ {
		parked, err := Fail(j)
		if err != nil || parked {
			t.Fatalf("fail %d: parked=%v err=%v", want, parked, err)
		}
		jobs, _ = List(dir)
		if len(jobs) != 1 || jobs[0].Attempts() != want {
			t.Fatalf("after fail %d: %+v", want, jobs)
		}
		j = jobs[0]
	}

	// Third failure parks the job in failed/.
	parked, err := Fail(j)
	if err != nil || !parked {
		t.Fatalf("third fail: parked=%v err=%v", parked, err)
	}
	if jobs, _ = List(dir); len(jobs) != 0 {
		t.Fatalf("parked job still listed: %+v", jobs)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "aaaa.json.f2")); err != nil {
		t.Errorf("parked file missing: %v", err)
	}

	// Done removes.
	p := writeJob(t, dir, "bbbb.json", "/tmp/b.jsonl")
	jobs, _ = List(dir)
	if err := Done(jobs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("done job still on disk")
	}
}

func TestListParksMalformedJob(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs, err := List(dir)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("jobs = %v, err %v", jobs, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "bad.json")); err != nil {
		t.Errorf("malformed job not parked: %v", err)
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	l, err := TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(dir); err != ErrLocked {
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	l.Unlock()
	l2, err := TryLock(dir)
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	l2.Unlock()

	// Stale lock is broken.
	lockPath := filepath.Join(dir, "learn.lock")
	if err := os.WriteFile(lockPath, []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	l3, err := TryLock(dir)
	if err != nil {
		t.Fatalf("stale lock not broken: %v", err)
	}
	l3.Unlock()
}

func TestCursors(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(transcript, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := LoadCursors(dir)
	if got := c.Get(transcript); got.Offset != 0 {
		t.Fatalf("fresh cursor = %+v", got)
	}
	c.Set(transcript, Cursor{Offset: 42, MinedAt: time.Now().UTC()})
	c.Set(filepath.Join(dir, "gone.jsonl"), Cursor{Offset: 7})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c2 := LoadCursors(dir)
	if got := c2.Get(transcript); got.Offset != 42 {
		t.Errorf("reloaded cursor = %+v", got)
	}
	if got := c2.Get(filepath.Join(dir, "gone.jsonl")); got.Offset != 0 {
		t.Errorf("vanished transcript's cursor survived: %+v", got)
	}
}

func TestShouldMine(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		cur        Cursor
		size       int64
		sessionEnd bool
		want       bool
	}{
		{"nothing new", Cursor{Offset: 100}, 100, true, false},
		{"session end mines any new bytes", Cursor{Offset: 100, MinedAt: now}, 101, true, true},
		{"periodic needs interval", Cursor{Offset: 0, MinedAt: now}, 1 << 20, false, false},
		{"periodic needs bytes", Cursor{Offset: 0, MinedAt: now.Add(-time.Hour)}, 100, false, false},
		{"periodic ok", Cursor{Offset: 0, MinedAt: now.Add(-time.Hour)}, 1 << 20, false, true},
	} {
		if got := ShouldMine(tc.cur, tc.size, tc.sessionEnd, now); got != tc.want {
			t.Errorf("%s: ShouldMine = %v, want %v", tc.name, got, tc.want)
		}
	}
}
