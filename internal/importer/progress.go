package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// mergeProgress records which merge units have fully staged, so an interrupted
// run (deadline, session cap, ctrl-C) can --resume without re-paying for the
// LLM calls that already succeeded. Lives beside staged/ in .import/ (which
// apply never walks); cleared on completion and by --force.
type mergeProgress struct {
	Done []string `json:"done"`
}

func progressPath(knowledgeDir string) string {
	return filepath.Join(knowledgeDir, ".import", "merge-progress.json")
}

// HasProgress reports whether an interrupted merge left resumable progress.
func HasProgress(knowledgeDir string) bool {
	_, err := os.Stat(progressPath(knowledgeDir))
	return err == nil
}

// readProgress is permissive: a missing or corrupt file means nothing is done
// — worst case a unit re-runs, which is safe (staged paths just overwrite).
func readProgress(path string) map[string]bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var p mergeProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		return map[string]bool{}
	}
	done := make(map[string]bool, len(p.Done))
	for _, k := range p.Done {
		done[k] = true
	}
	return done
}

// writeProgress persists after every unit — a tiny file rewrite versus the
// LLM calls it protects.
func writeProgress(path string, done map[string]bool) error {
	keys := make([]string, 0, len(done))
	for k := range done {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	raw, err := json.Marshal(mergeProgress{Done: keys})
	if err != nil {
		return fmt.Errorf("importer: encoding merge progress: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("importer: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("importer: writing merge progress: %w", err)
	}
	return nil
}
