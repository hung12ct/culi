package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cursor is one transcript's mining progress: everything before Offset has
// been mined once (plan §learning A — each region mined exactly once).
type Cursor struct {
	Offset  int64     `json:"offset"`
	MinedAt time.Time `json:"mined_at"`
}

// Cursors is the persistent transcript_path → Cursor map.
type Cursors struct {
	path string
	m    map[string]Cursor
}

// LoadCursors reads state/learn_cursors.json; a missing or corrupt file
// yields an empty map (worst case: some regions are mined twice — the dedup
// stage absorbs that).
func LoadCursors(stateDir string) *Cursors {
	c := &Cursors{path: filepath.Join(stateDir, "learn_cursors.json"), m: map[string]Cursor{}}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c.m)
	if c.m == nil {
		c.m = map[string]Cursor{}
	}
	return c
}

// Get returns the cursor for a transcript (zero value = never mined).
func (c *Cursors) Get(transcriptPath string) Cursor { return c.m[transcriptPath] }

// Set records progress for a transcript.
func (c *Cursors) Set(transcriptPath string, cur Cursor) { c.m[transcriptPath] = cur }

// Save writes the map atomically (temp + rename) and prunes entries whose
// transcript no longer exists, so the file cannot grow without bound.
func (c *Cursors) Save() error {
	for path := range c.m {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(c.m, path)
		}
	}
	raw, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return fmt.Errorf("queue: marshaling cursors: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("queue: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".cursors-*")
	if err != nil {
		return fmt.Errorf("queue: creating temp cursors: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("queue: writing cursors: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("queue: closing cursors: %w", err)
	}
	if err := os.Rename(name, c.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("queue: replacing cursors: %w", err)
	}
	return nil
}
