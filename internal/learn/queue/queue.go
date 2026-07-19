// Package queue manages the learn inbox: job files written by the SessionEnd
// hook, a lockfile so only one worker mines at a time, and per-transcript byte
// cursors. Deletion is parse-gated (ecosystem lesson): a job is removed only
// after a successfully processed mine — crashes and retries never lose
// observations. Three failures move the job to inbox/failed/ (circuit break).
package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxAttempts is the fail-ladder ceiling before a job is parked in failed/.
const maxAttempts = 3

// Job is one queued transcript, as enqueued by the SessionEnd hook.
type Job struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	EnqueuedAt     string `json:"enqueued_at"`

	path     string // job file on disk
	attempts int    // prior failed attempts, parsed from the .fN suffix
}

// Path returns the job file location; Attempts the prior failure count.
func (j Job) Path() string  { return j.path }
func (j Job) Attempts() int { return j.attempts }

// List reads every pending job (including previously failed retries), oldest
// first. A malformed job file is skipped, never fatal.
func List(inboxDir string) ([]Job, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("queue: reading inbox: %w", err)
	}
	var jobs []Job
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		attempts := 0
		base := name
		if i := strings.LastIndex(name, ".f"); i > 0 && strings.HasSuffix(name[:i], ".json") {
			n, err := strconv.Atoi(name[i+2:])
			if err != nil {
				continue
			}
			attempts, base = n, name[:i]
		}
		if !strings.HasSuffix(base, ".json") {
			continue
		}
		path := filepath.Join(inboxDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var j Job
		if err := json.Unmarshal(raw, &j); err != nil || j.TranscriptPath == "" {
			// Unreadable job: park it so the inbox cannot jam on one bad file.
			_ = park(inboxDir, path)
			continue
		}
		j.path, j.attempts = path, attempts
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].EnqueuedAt < jobs[b].EnqueuedAt })
	return jobs, nil
}

// Done removes a successfully processed job (parse-gated: call only after the
// mine result was parsed and persisted).
func Done(j Job) error {
	if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("queue: removing job: %w", err)
	}
	return nil
}

// Fail records one failed attempt. The job file survives under a .fN suffix
// until maxAttempts, then moves to inbox/failed/ for manual inspection.
// Returns true when the job was parked (no more retries).
func Fail(j Job) (parked bool, err error) {
	dir := filepath.Dir(j.path)
	next := j.attempts + 1
	if next >= maxAttempts {
		if err := park(dir, j.path); err != nil {
			return false, err
		}
		return true, nil
	}
	base := strings.TrimSuffix(filepath.Base(j.path), ".f"+strconv.Itoa(j.attempts))
	dst := filepath.Join(dir, base+".f"+strconv.Itoa(next))
	if err := os.Rename(j.path, dst); err != nil {
		return false, fmt.Errorf("queue: renaming failed job: %w", err)
	}
	return false, nil
}

// park moves a job file into inbox/failed/.
func park(inboxDir, path string) error {
	failed := filepath.Join(inboxDir, "failed")
	if err := os.MkdirAll(failed, 0o755); err != nil {
		return fmt.Errorf("queue: creating failed dir: %w", err)
	}
	if err := os.Rename(path, filepath.Join(failed, filepath.Base(path))); err != nil {
		return fmt.Errorf("queue: parking job: %w", err)
	}
	return nil
}

// throttle policy (plan §learning A): SessionEnd is the final flush and
// bypasses the interval; periodic (Stop-hook) mining would additionally
// require both minInterval and minNewBytes. All current jobs are session-end.
const (
	minInterval = 10 * time.Minute
	minNewBytes = 16 << 10
)

// ShouldMine decides whether a transcript has enough new content to be worth
// parsing. size is the transcript's current byte size.
func ShouldMine(cur Cursor, size int64, sessionEnd bool, now time.Time) bool {
	if size <= cur.Offset {
		return false // nothing new (or file shrank — cursor resets at read time)
	}
	if sessionEnd {
		return true
	}
	return now.Sub(cur.MinedAt) >= minInterval && size-cur.Offset >= minNewBytes
}
