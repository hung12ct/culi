package branchgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/knowledge"
)

// upsertCard writes one generated card at its deterministic path.
// Regeneration policy (C4):
//   - path holds a non-gen card (hand-authored or other pipeline) → skip
//   - same source hash → skip (byte-stable regen)
//   - gen card from older facts → overwrite (it derives from git, git moved)
func (g *Generator) upsertCard(repo, branch, factsHash string, c genCard) (id string, wrote bool, note string, err error) {
	typ := c.Type
	if typ != "rule" && typ != "pattern" {
		typ = "pattern"
	}
	slug := slugify(repo)
	if branch != "" {
		slug += "-" + slugify(branch)
	}
	slug += "-" + slugify(c.Slug)
	rel := typ + "s/gen-" + slug + ".md"
	id = strings.TrimSuffix(rel, ".md")

	kdir := config.KnowledgeDir(g.Base)
	abs := filepath.Join(kdir, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err == nil {
		prev, rerr := knowledge.ReadCard(kdir, rel)
		switch {
		case rerr != nil:
			// Unreadable ≠ overwritable: a hand-edit that broke the
			// frontmatter must not be clobbered (C4).
			return id, false, id + " (exists but unparseable — left alone)", nil
		case prev.Provenance == nil || prev.Provenance.Source != "gen":
			return id, false, id + " (exists, not gen-authored — left alone)", nil
		case prev.Provenance.SourceHash == factsHash:
			return id, false, id + " (unchanged)", nil
		}
	}

	scope := "repo:" + repo
	if branch != "" {
		scope = "branch:" + repo + "@" + branch
	}
	kw := c.Keywords
	if len(kw) > 5 {
		kw = kw[:5]
	}
	card := knowledge.Card{
		Type:     typ,
		Title:    strings.TrimSpace(c.Title),
		Summary:  strings.TrimSpace(c.Summary),
		Body:     strings.TrimSpace(c.Markdown),
		Scopes:   []string{scope},
		Triggers: knowledge.Triggers{Keywords: kw},
		// Explicit `culi gen` is user intent on the user's own history —
		// confirmed like save_lesson, and repo/branch-scoped so a weak card's
		// blast radius stays one repo.
		Status: "confirmed",
		Provenance: &knowledge.Provenance{
			Source: "gen", Model: g.Tier.Cheap.ModelName(), SourceHash: factsHash,
		},
	}
	rendered, err := knowledge.Render(card)
	if err != nil {
		return id, false, "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return id, false, "", fmt.Errorf("branchgen: creating card dir: %w", err)
	}
	if err := os.WriteFile(abs, rendered, 0o644); err != nil {
		return id, false, "", fmt.Errorf("branchgen: writing card: %w", err)
	}
	return id, true, "", nil
}

// genState maps "<repo>[@branch]" → facts hash of the last completed run.
type genState map[string]string

func genStatePath(stateDir string) string { return filepath.Join(stateDir, "gen.json") }

// loadGenState reads state/gen.json; missing/corrupt starts empty (worst
// case one redundant regen, which the per-artifact hashes then no-op).
func loadGenState(stateDir string) genState {
	st := genState{}
	raw, err := os.ReadFile(genStatePath(stateDir))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	if st == nil {
		st = genState{}
	}
	return st
}

// saveGenState writes atomically (temp + rename).
func saveGenState(stateDir string, st genState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("branchgen: marshaling gen state: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("branchgen: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, ".gen-*")
	if err != nil {
		return fmt.Errorf("branchgen: creating temp state: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("branchgen: writing gen state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("branchgen: closing gen state: %w", err)
	}
	if err := os.Rename(name, genStatePath(stateDir)); err != nil {
		os.Remove(name)
		return fmt.Errorf("branchgen: replacing gen state: %w", err)
	}
	return nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify mirrors the store-wide slug rules.
func slugify(s string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if slug == "" {
		return "x"
	}
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return slug
}
