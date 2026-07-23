package codexscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	healthFileName = "codex-scan.json"
	lockFileName   = "codex-scan.lock"
	staleLockAge   = 2 * time.Minute
)

// ErrScanLocked means another process is already discovering Codex rollouts.
// The automatic path treats it as a benign skip; an explicit manual scan
// surfaces it so two operators never race telemetry or queue writes.
var ErrScanLocked = errors.New("codexscan: scan already running")

// Health is the durable, content-free scanner breadcrumb consumed by doctor.
// It deliberately records counts and timing only—never rollout paths, prompts,
// repository names, or session IDs.
type Health struct {
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Discovered  int       `json:"discovered"`
	Queued      int       `json:"queued"`
	Skipped     int       `json:"skipped"`
	DurationMS  int64     `json:"duration_ms"`
	Error       string    `json:"error,omitempty"`
}

// LoadHealth reads the latest scanner breadcrumb. Missing state means the
// scanner has not run yet; malformed state is reported to doctor rather than
// silently presented as "never".
func LoadHealth(stateDir string) (Health, error) {
	var h Health
	raw, err := os.ReadFile(filepath.Join(stateDir, healthFileName))
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, fmt.Errorf("codexscan: reading health: %w", err)
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return Health{}, fmt.Errorf("codexscan: decoding health: %w", err)
	}
	return h, nil
}

// SaveHealth atomically replaces the scanner breadcrumb.
func SaveHealth(stateDir string, h Health) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("codexscan: creating state dir: %w", err)
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("codexscan: encoding health: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, ".codex-scan-health-*")
	if err != nil {
		return fmt.Errorf("codexscan: creating health temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("codexscan: writing health: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codexscan: closing health: %w", err)
	}
	if err := os.Rename(name, filepath.Join(stateDir, healthFileName)); err != nil {
		return fmt.Errorf("codexscan: replacing health: %w", err)
	}
	return nil
}

// Lease serializes discovery across Stop/SessionEnd workers. Release is
// idempotent so callers can defer it immediately after acquisition.
type Lease struct {
	path     string
	released bool
}

// Acquire obtains the scanner lease and applies minInterval to the last
// attempted scan. A zero interval is an unthrottled manual scan. due=false is
// a clean throttle result; ErrScanLocked means another scan owns the lease.
func Acquire(stateDir string, now time.Time, minInterval time.Duration) (*Lease, bool, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("codexscan: creating state dir: %w", err)
	}
	path := filepath.Join(stateDir, lockFileName)
	lease, err := createLease(path)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Stat(path)
		if statErr == nil && now.Sub(info.ModTime()) > staleLockAge {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				lease, err = createLease(path)
			}
		}
	}
	if errors.Is(err, os.ErrExist) {
		return nil, false, ErrScanLocked
	}
	if err != nil {
		return nil, false, fmt.Errorf("codexscan: acquiring scan lease: %w", err)
	}

	h, healthErr := LoadHealth(stateDir)
	if healthErr == nil && minInterval > 0 && !h.LastAttempt.IsZero() && now.Sub(h.LastAttempt) < minInterval {
		_ = lease.Release()
		return nil, false, nil
	}
	// Corrupt telemetry must not permanently disable recovery. The successful
	// scan will replace it; doctor exposes it until then.
	return lease, true, nil
}

func createLease(path string) (*Lease, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return &Lease{path: path}, nil
}

// Release relinquishes the scanner lease.
func (l *Lease) Release() error {
	if l == nil || l.released {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("codexscan: releasing scan lease: %w", err)
	}
	l.released = true
	return nil
}
