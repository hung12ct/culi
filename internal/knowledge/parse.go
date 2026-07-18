package knowledge

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter is the on-disk YAML schema. Unknown keys are tolerated (OKF
// compatibility); missing optional fields default. Only Title is required —
// everything else degrades.
type frontmatter struct {
	ID         string      `yaml:"id,omitempty"`
	Type       string      `yaml:"type,omitempty"`
	Title      string      `yaml:"title,omitempty"`
	Summary    string      `yaml:"summary,omitempty"`
	Scope      []string    `yaml:"scope,omitempty"`
	Key        string      `yaml:"key,omitempty"`
	Triggers   Triggers    `yaml:"triggers,omitempty"`
	Aliases    []string    `yaml:"aliases,omitempty"`
	Baseline   bool        `yaml:"baseline,omitempty"`
	Status     string      `yaml:"status,omitempty"`
	Export     *ExportMeta `yaml:"export,omitempty"`
	Provenance *Provenance `yaml:"provenance,omitempty"`
}

var fmDelim = []byte("---")

// Parse builds a Card from raw file bytes. relPath is the path relative to the
// knowledge dir (it derives the default ID and type). Errors are returned for
// the caller to log-and-skip — a bad card must never fail an index run.
func Parse(relPath string, raw []byte) (Card, error) {
	fm, body, err := splitFrontmatter(raw)
	if err != nil {
		return Card{}, fmt.Errorf("knowledge: %s: %w", relPath, err)
	}
	var meta frontmatter
	if len(fm) > 0 {
		if err := yaml.Unmarshal(fm, &meta); err != nil {
			return Card{}, fmt.Errorf("knowledge: %s: parsing frontmatter: %w", relPath, err)
		}
	}

	c := Card{
		ID:          meta.ID,
		Path:        relPath,
		Type:        meta.Type,
		Title:       strings.TrimSpace(meta.Title),
		Summary:     strings.TrimSpace(meta.Summary),
		Body:        strings.TrimSpace(string(body)),
		Scopes:      meta.Scope,
		Key:         meta.Key,
		Triggers:    meta.Triggers,
		Aliases:     meta.Aliases,
		Baseline:    meta.Baseline,
		Status:      meta.Status,
		ContentHash: hashContent(raw),
		Export:      meta.Export,
		Provenance:  meta.Provenance,
	}
	if c.ID == "" {
		c.ID = defaultID(relPath)
	}
	if c.Type == "" {
		c.Type = defaultType(relPath)
	}
	if c.Title == "" {
		return Card{}, fmt.Errorf("knowledge: %s: missing title", relPath)
	}
	if c.Summary == "" {
		// Degrade: first body line stands in for the summary granularity.
		c.Summary = firstLine(c.Body)
	}
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"global"}
	}
	c.TokSummary = EstimateTokens(c.Summary)
	c.TokBody = EstimateTokens(c.Body)
	return c, nil
}

// splitFrontmatter separates a leading "---\n...\n---\n" block from the body.
// A file without frontmatter is all body.
func splitFrontmatter(raw []byte) (fm, body []byte, err error) {
	trimmed := bytes.TrimLeft(raw, "\uFEFF\n\r\t ")
	if !bytes.HasPrefix(trimmed, fmDelim) {
		return nil, raw, nil
	}
	rest := trimmed[len(fmDelim):]
	rest = bytes.TrimPrefix(rest, []byte("\r"))
	if len(rest) == 0 || rest[0] != '\n' {
		return nil, raw, nil // "---something" is body, not a delimiter
	}
	rest = rest[1:]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, nil, fmt.Errorf("unterminated frontmatter")
	}
	fm = rest[:end]
	body = rest[end+len("\n---"):]
	// Drop the delimiter's trailing newline (and optional \r).
	body = bytes.TrimPrefix(body, []byte("\r"))
	body = bytes.TrimPrefix(body, []byte("\n"))
	return fm, body, nil
}

// defaultID derives "rules/go-error-wrapping" from "rules/go-error-wrapping.md"
// and "skills/tdd" from "skills/tdd/SKILL.md".
func defaultID(relPath string) string {
	p := filepath.ToSlash(relPath)
	if strings.HasSuffix(p, "/SKILL.md") {
		return strings.TrimSuffix(p, "/SKILL.md")
	}
	return strings.TrimSuffix(p, filepath.Ext(p))
}

// defaultType maps the top-level directory to a card type: rules/ → rule.
func defaultType(relPath string) string {
	p := filepath.ToSlash(relPath)
	dir, _, ok := strings.Cut(p, "/")
	if !ok {
		return "rule"
	}
	switch dir {
	case "rules":
		return "rule"
	case "styles":
		return "style"
	case "lessons":
		return "lesson"
	case "patterns":
		return "pattern"
	case "skills":
		return "skill"
	case "agents":
		return "agent"
	default:
		return "rule"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
