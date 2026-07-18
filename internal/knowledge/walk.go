package knowledge

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileInfo describes one card file on disk, cheap enough to stat the whole
// store per index run (100s–1000s of files).
type FileInfo struct {
	RelPath string
	MTime   int64
	Size    int64
}

// Walk lists every card file under dir. Card files are *.md, excluding dot
// dirs (.git, .import, .drafts — staging areas are not indexed). For skills,
// only SKILL.md is a card; sibling files are attachments (Phase 3).
func Walk(dir string) ([]FileInfo, error) {
	var out []FileInfo
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Under skills/, only SKILL.md files are cards.
		if strings.HasPrefix(rel, "skills/") && name != "SKILL.md" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, FileInfo{RelPath: rel, MTime: info.ModTime().UnixNano(), Size: info.Size()})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil // empty store is not an error
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: walking %s: %w", dir, err)
	}
	return out, nil
}

// ReadCard loads and parses one card file.
func ReadCard(dir, relPath string) (Card, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relPath)))
	if err != nil {
		return Card{}, fmt.Errorf("knowledge: reading %s: %w", relPath, err)
	}
	return Parse(relPath, raw)
}
