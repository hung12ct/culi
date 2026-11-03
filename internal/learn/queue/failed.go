package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Parked is one job in inbox/failed/, as shown by `culi learn --failed`.
type Parked struct {
	Job
	File             string // file name inside inbox/failed/
	TranscriptExists bool   // false means a retry can never succeed
	Malformed        bool   // the job file did not parse; only File is set
}

// Failed lists parked jobs, newest failure first. Unreadable files are
// reported as Malformed rather than skipped, so the count matches the
// statusline's.
func Failed(inboxDir string) ([]Parked, error) {
	dir := filepath.Join(inboxDir, "failed")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("queue: reading failed jobs: %w", err)
	}
	var out []Parked
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), ".json") {
			continue
		}
		p := Parked{File: e.Name()}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || json.Unmarshal(raw, &p.Job) != nil || p.TranscriptPath == "" {
			p.Malformed = true
			out = append(out, p)
			continue
		}
		_, statErr := os.Stat(p.TranscriptPath)
		p.TranscriptExists = statErr == nil
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return failedKey(out[a]) > failedKey(out[b]) })
	return out, nil
}

func failedKey(p Parked) string {
	if p.FailedAt != "" {
		return p.FailedAt
	}
	return p.EnqueuedAt
}

// RetryFailed moves every parked job whose transcript still exists back into
// the inbox with a fresh retry ladder, keeping LastError for context. Jobs
// whose transcript is gone, and malformed files, stay parked.
func RetryFailed(inboxDir string) (int, error) {
	parked, err := Failed(inboxDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range parked {
		if p.Malformed || !p.TranscriptExists {
			continue
		}
		base := p.File
		if i := strings.LastIndex(base, ".json"); i >= 0 {
			base = base[:i+len(".json")]
		}
		if err := writeJobFile(filepath.Join(inboxDir, base), p.Job); err != nil {
			return n, err
		}
		if err := os.Remove(filepath.Join(inboxDir, "failed", p.File)); err != nil && !os.IsNotExist(err) {
			return n, fmt.Errorf("queue: removing retried job: %w", err)
		}
		n++
	}
	return n, nil
}

// ClearFailed deletes every parked job. Job files are pointers to
// transcripts, so clearing never deletes session content.
func ClearFailed(inboxDir string) (int, error) {
	parked, err := Failed(inboxDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range parked {
		if err := os.Remove(filepath.Join(inboxDir, "failed", p.File)); err != nil && !os.IsNotExist(err) {
			return n, fmt.Errorf("queue: clearing failed job: %w", err)
		}
		n++
	}
	return n, nil
}
