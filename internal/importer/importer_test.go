package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
)

// writeRepo builds a fake repo with .claude artifacts. files maps repo-relative
// paths to content.
func writeRepo(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const agentFM = "---\nname: %NAME%\ndescription: does %NAME% things\n---\n\n"

func agentFile(name, body string) string {
	return strings.ReplaceAll(agentFM, "%NAME%", name) + body
}

// fixtureRepos builds three repos covering every classification.
func fixtureRepos(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "alpha"), filepath.Join(dir, "beta"), filepath.Join(dir, "gamma")
	writeRepo(t, a, map[string]string{
		".claude/agents/same.md":      agentFile("same", "Line one.\nLine two.\n"),
		".claude/agents/super.md":     agentFile("super", "Base guidance.\nExtra alpha-only guidance.\n"),
		".claude/skills/div/SKILL.md": "---\ndescription: diverged skill\n---\n\nShared step.\nAlpha special step.\n",
		"CLAUDE.md":                   "# Alpha\n\nUse table tests.\nBuild with make.\n",
	})
	writeRepo(t, b, map[string]string{
		".claude/agents/same.md":      agentFile("same", "Line one.\n\nLine two.\n"), // blank-line drift only
		".claude/agents/super.md":     agentFile("super", "Base guidance.\n"),
		".claude/skills/div/SKILL.md": "---\ndescription: diverged skill\n---\n\nShared step.\nBeta different step.\n",
	})
	writeRepo(t, c, map[string]string{
		".claude/skills/solo/SKILL.md": "---\ndescription: gamma-only skill\n---\n\nOnly here.\n",
		".claude/skills/solo/extra.md": "attachment content\n",
	})
	return []string{a, b, c}
}

func clusterByKey(t *testing.T, rep Report, key string) Cluster {
	t.Helper()
	for _, cl := range rep.Clusters {
		if cl.Key == key {
			return cl
		}
	}
	t.Fatalf("cluster %q not found in %+v", key, rep.Clusters)
	return Cluster{}
}

func TestScanClassifies(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := clusterByKey(t, rep, "agent/same"); got.Class != "identical" {
		t.Errorf("same: %+v", got)
	}
	if got := clusterByKey(t, rep, "agent/super"); got.Class != "superset" || got.Canonical != "alpha" {
		t.Errorf("super: class=%s canonical=%s", got.Class, got.Canonical)
	}
	div := clusterByKey(t, rep, "skill/div")
	if div.Class != "diverged" || div.Similarity <= 0 || div.Similarity >= 1 {
		t.Errorf("div: class=%s sim=%f", div.Class, div.Similarity)
	}
	solo := clusterByKey(t, rep, "skill/solo")
	if solo.Class != "unique" || len(solo.Items[0].Attachments) != 1 {
		t.Errorf("solo: %+v", solo)
	}
	if solo.Items[0].Description != "gamma-only skill" {
		t.Errorf("description not parsed: %q", solo.Items[0].Description)
	}
	if len(rep.ClaudeMD) != 1 || rep.ClaudeMD[0].Repo != "alpha" {
		t.Errorf("claude.md items: %+v", rep.ClaudeMD)
	}
}

func TestScanReportRoundTrip(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	if _, err := WriteReport(kdir, rep); err != nil {
		t.Fatal(err)
	}
	back, err := ReadReport(kdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Clusters) != len(rep.Clusters) || len(back.ClaudeMD) != 1 {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestMergeMechanicalOnly(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	res, err := Merge(context.Background(), kdir, rep, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// diverged skill + CLAUDE.md are LLM work: skipped without a merger.
	if len(res.Skipped) != 2 {
		t.Errorf("skipped = %v", res.Skipped)
	}
	staged := filepath.Join(kdir, ".import", "staged")
	card, err := knowledge.ReadCard(staged, "agents/same.md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Export == nil || card.Export.Kind != "agent" || !strings.Contains(card.Export.Frontmatter, "name: same") {
		t.Errorf("export meta: %+v", card.Export)
	}
	if card.Provenance == nil || len(card.Provenance.MergedFrom) != 2 {
		t.Errorf("provenance: %+v", card.Provenance)
	}
	if card.Scopes[0] != "global" || card.Summary != "does same things" {
		t.Errorf("card: scopes=%v summary=%q", card.Scopes, card.Summary)
	}
	// Unique skill is repo-scoped and its attachment travels with it.
	solo, err := knowledge.ReadCard(staged, "skills/solo/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if solo.Scopes[0] != "repo:gamma" {
		t.Errorf("solo scopes: %v", solo.Scopes)
	}
	if _, err := os.Stat(filepath.Join(staged, "skills", "solo", "extra.md")); err != nil {
		t.Errorf("attachment not staged: %v", err)
	}
	// Superset keeps the alpha copy (its extra line survives).
	super, err := knowledge.ReadCard(staged, "agents/super.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(super.Body, "Extra alpha-only guidance.") {
		t.Errorf("superset body lost lines: %q", super.Body)
	}
}

// fakeMerger returns canned LLM output.
type fakeMerger struct{}

func (fakeMerger) MergeCluster(_ context.Context, in ClusterInput) (ClusterMerge, error) {
	return ClusterMerge{
		CanonicalBody: "Merged canonical body.",
		Residues:      []Residue{{Repo: in.Copies[0].Repo, Title: "Alpha residue", Summary: "alpha only", Body: "Alpha special step."}},
		Notes:         "check me",
		Usage:         Usage{Prompt: 100, Completion: 50},
	}, nil
}

func (fakeMerger) DecomposeClaudeMD(_ context.Context, in ClaudeMDInput) (Decomposition, error) {
	return Decomposition{
		Cards: []DecomposedCard{
			{Slug: "table-tests", Type: "rule", Title: "Use table tests", Summary: "table tests", Scope: "lang:go", Body: "Use table tests."},
			{Slug: "evil-scope", Type: "rule", Title: "Scope escape", Summary: "x", Scope: "repo:other", Body: "Should collapse."},
		},
		Residual: "# " + in.Repo + "\n\nBuild with make.\n",
		Usage:    Usage{Prompt: 10, Completion: 5},
	}, nil
}

func (fakeMerger) ModelName() string { return "fake-model" }

func TestMergeWithLLM(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	res, err := Merge(context.Background(), kdir, rep, fakeMerger{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v", res.Skipped)
	}
	if res.Usage.Prompt != 110 || res.Usage.Completion != 55 {
		t.Errorf("usage = %+v", res.Usage)
	}
	staged := filepath.Join(kdir, ".import", "staged")
	div, err := knowledge.ReadCard(staged, "skills/div/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if div.Body != "Merged canonical body." || div.Provenance.Model != "fake-model" {
		t.Errorf("diverged card: body=%q prov=%+v", div.Body, div.Provenance)
	}
	if _, err := knowledge.ReadCard(staged, "rules/div-alpha.md"); err != nil {
		t.Errorf("residue not staged: %v", err)
	}
	// Scope escape must collapse back to the source repo.
	evil, err := knowledge.ReadCard(staged, "rules/evil-scope.md")
	if err != nil {
		t.Fatal(err)
	}
	if evil.Scopes[0] != "repo:alpha" {
		t.Errorf("scope not collapsed: %v", evil.Scopes)
	}
	if raw, err := os.ReadFile(filepath.Join(staged, "residual", "alpha.CLAUDE.md")); err != nil || !strings.Contains(string(raw), "Build with make.") {
		t.Errorf("residual: %v %q", err, raw)
	}
	// Re-merge without force must refuse the dirty staging area.
	if _, err := Merge(context.Background(), kdir, rep, nil, false); err == nil {
		t.Error("expected non-empty staging refusal")
	}
	if _, err := Merge(context.Background(), kdir, rep, nil, true); err != nil {
		t.Errorf("force re-merge: %v", err)
	}
}

func TestApply(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	if _, err := Merge(context.Background(), kdir, rep, fakeMerger{}, false); err != nil {
		t.Fatal(err)
	}
	// Pre-existing different card at a staged path → conflict.
	conflictPath := filepath.Join(kdir, "agents", "same.md")
	if err := os.MkdirAll(filepath.Dir(conflictPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictPath, []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(kdir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "agents/same.md" {
		t.Errorf("conflicts = %v", res.Conflicts)
	}
	if len(res.Residual) != 1 {
		t.Errorf("residual = %v", res.Residual)
	}
	if _, err := os.Stat(filepath.Join(kdir, "skills", "div", "SKILL.md")); err != nil {
		t.Errorf("apply did not move card: %v", err)
	}
	if raw, _ := os.ReadFile(conflictPath); string(raw) != "hand-edited\n" {
		t.Errorf("conflict destination overwritten: %q", raw)
	}
	// Force resolves the conflict; staging drains except residual.
	res, err = Apply(kdir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("force conflicts = %v", res.Conflicts)
	}
	if raw, _ := os.ReadFile(conflictPath); string(raw) == "hand-edited\n" {
		t.Error("force did not overwrite")
	}
	if _, err := os.Stat(filepath.Join(kdir, ".import", "staged", "residual")); err != nil {
		t.Errorf("residual removed from staging: %v", err)
	}
}
