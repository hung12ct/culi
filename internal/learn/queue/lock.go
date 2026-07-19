package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// staleLock is how old a lockfile may be before it is presumed abandoned (a
// crashed worker) and broken. Mining a whole inbox takes seconds-to-minutes;
// 30 minutes is far beyond any live run.
const staleLock = 30 * time.Minute

// ErrLocked reports another live worker holds the lock — the caller should
// simply exit; the queue drains on the next run.
var ErrLocked = errors.New("queue: another learn worker is running")

// Lock is a held lockfile.
type Lock struct{ path string }

// TryLock acquires state/learn.lock exclusively. A stale lockfile (older than
// staleLock) is broken once and re-acquired.
func TryLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("queue: creating state dir: %w", err)
	}
	path := filepath.Join(stateDir, "learn.lock")
	for attempt := range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return &Lock{path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("queue: creating lock: %w", err)
		}
		st, serr := os.Stat(path)
		if serr != nil || time.Since(st.ModTime()) < staleLock || attempt > 0 {
			return nil, ErrLocked
		}
		_ = os.Remove(path) // stale: break once and retry
	}
	return nil, ErrLocked
}

// Unlock releases the lockfile.
func (l *Lock) Unlock() {
	if l != nil {
		_ = os.Remove(l.path)
	}
}
