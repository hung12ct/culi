package codexscan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthRoundTripAndThrottle(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	want := Health{
		LastAttempt: now, LastSuccess: now, Mode: "auto",
		Discovered: 5, Queued: 2, Skipped: 1, DurationMS: 42,
	}
	if err := SaveHealth(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHealth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastAttempt.Equal(now) || got.Queued != 2 || got.Mode != "auto" {
		t.Fatalf("health = %+v", got)
	}
	if lease, due, err := Acquire(dir, now.Add(time.Minute), 10*time.Minute); err != nil || due || lease != nil {
		t.Fatalf("throttled acquire = lease %v due %v err %v", lease, due, err)
	}
	lease, due, err := Acquire(dir, now.Add(11*time.Minute), 10*time.Minute)
	if err != nil || !due || lease == nil {
		t.Fatalf("due acquire = lease %v due %v err %v", lease, due, err)
	}
	if _, _, err := Acquire(dir, now.Add(11*time.Minute), 0); !errors.Is(err, ErrScanLocked) {
		t.Fatalf("concurrent acquire = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireBreaksStaleLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(path, []byte("dead"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-staleLockAge - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lease, due, err := Acquire(dir, now, 0)
	if err != nil || !due || lease == nil {
		t.Fatalf("stale acquire = lease %v due %v err %v", lease, due, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}
