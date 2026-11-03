// Package queue manages the learn inbox: job files written by the SessionEnd
// hook, a lockfile so only one worker mines at a time, and per-transcript byte
// cursors. Deletion is parse-gated (ecosystem lesson): a job is removed only
// after a successfully processed mine — crashes and retries never lose
// observations. Three failures move the job to inbox/failed/ (circuit break).
package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/harness"
)

// maxAttempts is the fail-ladder ceiling before a job is parked in failed/.
const maxAttempts = 3

// Job is one queued transcript, as enqueued by the SessionEnd hook.
type Job struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	Source         harness.Harness `json:"source,omitempty"`
	Trigger        string          `json:"trigger,omitempty"`
	EnqueuedAt     string          `json:"enqueued_at"`
	// LastError and FailedAt record the most recent failed attempt, so a
	// parked job says why it was parked instead of just that it was.
	LastError string `json:"last_error,omitempty"`
	FailedAt  string `json:"failed_at,omitempty"`

	path     string // job file on disk
	attempts int    // prior failed attempts, parsed from the .fN suffix
}

// Enqueue atomically writes one stable transcript pointer. The filename is
// derived from source+session+path, so re-running a history scan (or a hook
// re-delivering the same session) overwrites in place instead of creating a
// duplicate job. Codex can reach this boundary through either a direct hook
// transcript path or the rollout scanner, so the stable key is an active
// cross-mechanism safety property.
func Enqueue(inboxDir string, job Job) error {
	if job.TranscriptPath == "" {
		return nil
	}
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		return fmt.Errorf("queue: creating inbox: %w", err)
	}
	key := job.EffectiveSource().String() + job.SessionID + job.TranscriptPath
	sum := sha256.Sum256([]byte(key))
	return writeJobFile(filepath.Join(inboxDir, hex.EncodeToString(sum[:8])+".json"), job)
}

// writeJobFile atomically writes job as JSON to path (temp file + rename in the
// same directory), so a reader never sees a half-written job.
func writeJobFile(path string, job Job) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("queue: marshaling job: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".job-*.tmp")
	if err != nil {
		return fmt.Errorf("queue: creating temporary job: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("queue: setting job permissions: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("queue: writing temporary job: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("queue: closing temporary job: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("queue: replacing job: %w", err)
	}
	return nil
}

// ScannerBlockedTranscripts returns transcript paths that a rollout scanner
// must not enqueue again. It includes active jobs, retry-renamed .json.fN
// jobs, and jobs parked after exhausting their retry ladder. Without the
// parked set, a periodic scan would silently resurrect a circuit-broken job
// every ten minutes. This read-only helper never repairs malformed entries.
func ScannerBlockedTranscripts(inboxDir string) map[string]struct{} {
	out := map[string]struct{}{}
	collectTranscriptPaths(out, inboxDir)
	collectTranscriptPaths(out, filepath.Join(inboxDir, "failed"))
	return out
}

func collectTranscriptPaths(out map[string]struct{}, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var job struct {
			TranscriptPath string `json:"transcript_path"`
		}
		if json.Unmarshal(raw, &job) == nil && job.TranscriptPath != "" {
			out[job.TranscriptPath] = struct{}{}
		}
	}
}

// Path returns the job file location; Attempts the prior failure count.
func (j Job) Path() string  { return j.path }
func (j Job) Attempts() int { return j.attempts }

func (j Job) EffectiveSource() harness.Harness {
	if j.Source == "" {
		return harness.Default
	}
	return j.Source
}

// Missing trigger is a job from the old SessionEnd-only queue format.
func (j Job) IsFinal() bool { return j.Trigger == "" || j.Trigger == "session-end" }

// List reads every pending job (including previously failed retries), newest
// first. A malformed job file is parked, never fatal.
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
	// Newest first: recent conversations carry the most relevant lessons, and a
	// per-run limit (learn.max_jobs_per_run) should keep the freshest ones.
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].EnqueuedAt > jobs[b].EnqueuedAt })
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

// maxErrorLen bounds the recorded failure cause; a backend can return a whole
// HTML error page.
const maxErrorLen = 600

// Fail records one failed attempt and its cause. The job file survives under a
// .fN suffix until maxAttempts, then moves to inbox/failed/ for manual
// inspection. Returns true when the job was parked (no more retries).
func Fail(j Job, cause error) (parked bool, err error) {
	if cause != nil {
		j.LastError = cause.Error()
		if len(j.LastError) > maxErrorLen {
			j.LastError = j.LastError[:maxErrorLen] + "…"
		}
	}
	j.FailedAt = time.Now().UTC().Format(time.RFC3339)
	dir := filepath.Dir(j.path)
	base := strings.TrimSuffix(filepath.Base(j.path), ".f"+strconv.Itoa(j.attempts))
	next := j.attempts + 1
	dst := filepath.Join(dir, base+".f"+strconv.Itoa(next))
	if next >= maxAttempts {
		failed := filepath.Join(dir, "failed")
		if err := os.MkdirAll(failed, 0o755); err != nil {
			return false, fmt.Errorf("queue: creating failed dir: %w", err)
		}
		dst = filepath.Join(failed, base+".f"+strconv.Itoa(j.attempts))
	}
	if err := writeJobFile(dst, j); err != nil {
		return false, err
	}
	if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("queue: removing failed job: %w", err)
	}
	return next >= maxAttempts, nil
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

// throttle policy (plan §learning A): SessionEnd and history-backfill jobs are
// final flushes and bypass the interval; periodic Stop-hook jobs require both
// minInterval and minNewBytes.
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
