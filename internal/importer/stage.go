package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hung12ct/culi/internal/knowledge"
)

// stageArtifact writes one agent/skill card into staging, plus skill
// attachments copied from the canonical repo.
func stageArtifact(staged string, cl Cluster, canon Item, fmRaw, body string, scope, mergedFrom []string, model string) (string, error) {
	rel := filepath.Join("agents", cl.Name+".md")
	if cl.Kind == "skill" {
		rel = filepath.Join("skills", cl.Name, "SKILL.md")
	}
	card := knowledge.Card{
		Path:    filepath.ToSlash(rel),
		Type:    cl.Kind,
		Title:   cl.Name,
		Summary: canon.Description,
		Body:    body,
		Scopes:  scope,
		Export: &knowledge.ExportMeta{
			Kind:        cl.Kind,
			Name:        cl.Name,
			Frontmatter: strings.TrimSpace(fmRaw),
			Attachments: canon.Attachments,
		},
		Provenance: &knowledge.Provenance{Source: "import", MergedFrom: mergedFrom, Model: model},
	}
	if err := writeCard(staged, rel, card); err != nil {
		return "", err
	}
	for _, att := range canon.Attachments {
		src := filepath.Join(filepath.Dir(canon.Path), filepath.FromSlash(att))
		raw, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("importer: reading attachment %s: %w", src, err)
		}
		if err := writeStaged(staged, filepath.Join(filepath.Dir(rel), filepath.FromSlash(att)), raw); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(rel), nil
}

// stageResidue writes repo-specific guidance the LLM carved out of a diverged
// cluster as a repo-scoped rule card.
func stageResidue(staged string, cl Cluster, r Residue) (string, error) {
	rel := filepath.Join("rules", slugify(cl.Name+"-"+r.Repo)+".md")
	title := r.Title
	if title == "" {
		title = cl.Name + " (" + r.Repo + ")"
	}
	card := knowledge.Card{
		Path:       filepath.ToSlash(rel),
		Type:       "rule",
		Title:      title,
		Summary:    r.Summary,
		Body:       r.Body,
		Scopes:     []string{"repo:" + r.Repo},
		Provenance: &knowledge.Provenance{Source: "import", MergedFrom: []string{r.Repo}},
	}
	return filepath.ToSlash(rel), writeCard(staged, rel, card)
}

// stageDecomposed writes one card extracted from a CLAUDE.md. Scopes outside
// the allowed set collapse to repo:<repo> — the LLM must never widen scope.
func stageDecomposed(staged, repo string, dc DecomposedCard, model string) (string, error) {
	typ := dc.Type
	switch typ {
	case "rule", "style", "pattern":
	default:
		typ = "rule"
	}
	scope := dc.Scope
	if scope != "global" && !strings.HasPrefix(scope, "lang:") && scope != "repo:"+repo {
		scope = "repo:" + repo
	}
	slug := slugify(dc.Slug)
	if slug == "" {
		slug = slugify(dc.Title)
	}
	if slug == "" {
		return "", fmt.Errorf("importer: decomposed card from %s has no usable slug or title", repo)
	}
	rel := filepath.Join(typ+"s", slug+".md")
	card := knowledge.Card{
		Path:       filepath.ToSlash(rel),
		Type:       typ,
		Title:      dc.Title,
		Summary:    dc.Summary,
		Body:       dc.Body,
		Scopes:     []string{scope},
		Aliases:    dc.Keywords,
		Provenance: &knowledge.Provenance{Source: "import", MergedFrom: []string{repo}, Model: model},
	}
	return filepath.ToSlash(rel), writeCard(staged, rel, card)
}

// writeCard renders and writes a card file under the staging root.
func writeCard(staged, rel string, card knowledge.Card) error {
	if card.Title == "" {
		return fmt.Errorf("importer: card %s has no title", rel)
	}
	raw, err := knowledge.Render(card)
	if err != nil {
		return fmt.Errorf("importer: %w", err)
	}
	return writeStaged(staged, rel, raw)
}

// writeStaged writes bytes under the staging root, creating parent dirs.
func writeStaged(staged, rel string, raw []byte) error {
	path := filepath.Join(staged, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("importer: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("importer: writing %s: %w", path, err)
	}
	return nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases and collapses non-alphanumerics to single dashes.
func slugify(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
