package knowledge

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Render serializes a Card back to file bytes: YAML frontmatter + body.
// yaml.Marshal handles quoting, so summaries containing ": " or quotes can
// never produce an unparseable card (a hand-rolled writer once did).
// Round-tripping through Parse loses nothing culi reads, but drops unknown
// frontmatter keys — Render is for cards culi authors (import, learning),
// never for rewriting hand-edited files.
func Render(c Card) ([]byte, error) {
	fm := frontmatter{
		ID:           c.ID,
		Type:         c.Type,
		Title:        c.Title,
		Summary:      c.Summary,
		Scope:        c.Scopes,
		Key:          c.Key,
		Triggers:     c.Triggers,
		Aliases:      c.Aliases,
		Baseline:     c.Baseline,
		Status:       c.Status,
		Observations: c.Observations,
		Supersedes:   c.Supersedes,
		Export:       c.Export,
		Provenance:   c.Provenance,
	}
	if fm.ID == defaultID(c.Path) {
		fm.ID = "" // derivable — keep frontmatter minimal
	}
	if fm.Type == defaultType(c.Path) {
		fm.Type = ""
	}
	meta, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("knowledge: rendering %s: %w", c.Path, err)
	}
	var b bytes.Buffer
	b.Grow(len(meta) + len(c.Body) + 16)
	b.WriteString("---\n")
	b.Write(meta)
	b.WriteString("---\n\n")
	b.WriteString(c.Body)
	if len(c.Body) > 0 && c.Body[len(c.Body)-1] != '\n' {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// SplitFrontmatter separates a leading "---" YAML block from the body of any
// markdown file (cards, but also Claude Code agent/skill files during import).
// A file without frontmatter returns nil fm and the full raw as body.
func SplitFrontmatter(raw []byte) (fm, body []byte, err error) {
	return splitFrontmatter(raw)
}
