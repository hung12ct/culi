package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
)

// UpdateFile parses one card file, applies mutate, and rewrites it atomically
// (temp + rename). ONLY for culi-authored cards — Render drops frontmatter
// keys culi does not model, so a hand-edited file must never pass through
// here (C4). Callers gate on Provenance.Source before calling.
func UpdateFile(knowledgeDir, relPath string, mutate func(*Card)) error {
	abs := filepath.Join(knowledgeDir, filepath.FromSlash(relPath))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("knowledge: reading %s: %w", relPath, err)
	}
	card, err := Parse(relPath, raw)
	if err != nil {
		return err
	}
	mutate(&card)
	rendered, err := Render(card)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".card-*")
	if err != nil {
		return fmt.Errorf("knowledge: creating temp for %s: %w", relPath, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(rendered); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("knowledge: writing %s: %w", relPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("knowledge: closing temp for %s: %w", relPath, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return fmt.Errorf("knowledge: chmod %s: %w", relPath, err)
	}
	if err := os.Rename(name, abs); err != nil {
		os.Remove(name)
		return fmt.Errorf("knowledge: replacing %s: %w", relPath, err)
	}
	return nil
}
