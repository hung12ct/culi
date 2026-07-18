package importer

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ApplyResult reports what apply moved into knowledge/ and what it refused.
type ApplyResult struct {
	Applied   []string // now live in knowledge/
	Conflicts []string // destination exists with different content; kept staged
	Residual  []string // residual CLAUDE.md files left for manual placement
}

// Apply moves reviewed staged files into knowledge/ proper. It is the single
// gate between LLM output and the canonical store (contract C4): an existing
// destination with different content is a conflict and stays staged unless
// force. Identical destinations are treated as already applied. residual/
// files are never applied — they belong in their repos, listed for the user.
func Apply(knowledgeDir string, force bool) (ApplyResult, error) {
	staged := filepath.Join(knowledgeDir, ".import", "staged")
	var res ApplyResult
	err := filepath.WalkDir(staged, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(staged, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "residual/") {
			res.Residual = append(res.Residual, rel)
			return nil
		}
		switch applyOne(staged, knowledgeDir, rel, force) {
		case applied:
			res.Applied = append(res.Applied, rel)
		case conflicted:
			res.Conflicts = append(res.Conflicts, rel)
		case failed:
			return fmt.Errorf("importer: applying %s failed", rel)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return res, fmt.Errorf("importer: nothing staged — run `culi import merge` first")
	}
	if err != nil {
		return res, fmt.Errorf("importer: applying staged files: %w", err)
	}
	pruneEmptyDirs(staged)
	sort.Strings(res.Applied)
	sort.Strings(res.Conflicts)
	return res, nil
}

type applyOutcome int

const (
	applied applyOutcome = iota
	conflicted
	failed
)

// applyOne moves one staged file to its destination under knowledgeDir.
func applyOne(staged, knowledgeDir, rel string, force bool) applyOutcome {
	src := filepath.Join(staged, filepath.FromSlash(rel))
	dst := filepath.Join(knowledgeDir, filepath.FromSlash(rel))
	if existing, err := os.ReadFile(dst); err == nil {
		srcRaw, err := os.ReadFile(src)
		if err != nil {
			return failed
		}
		if bytes.Equal(existing, srcRaw) {
			_ = os.Remove(src) // already applied; just drain staging
			return applied
		}
		if !force {
			return conflicted
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return failed
	}
	if err := os.Rename(src, dst); err != nil {
		return failed
	}
	return applied
}

// pruneEmptyDirs best-effort removes emptied staging directories so a fully
// applied staging area disappears (residual/ survives while it has files).
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // deepest first
	for _, d := range dirs {
		_ = os.Remove(d) // fails (kept) when non-empty
	}
}
