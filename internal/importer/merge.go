package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/knowledge"
)

// Merger reconciles content the mechanical path cannot: diverged clusters and
// CLAUDE.md decomposition. Consumer-side seam — the gopheragent-backed
// implementation lives in llm.go; tests use a fake. A nil Merger is valid:
// mechanical clusters still stage, LLM work is reported as skipped.
type Merger interface {
	MergeCluster(ctx context.Context, in ClusterInput) (ClusterMerge, error)
	DecomposeClaudeMD(ctx context.Context, in ClaudeMDInput) (Decomposition, error)
}

// ClusterInput carries the drifted copies of one artifact, bodies only —
// frontmatter is reconciled mechanically from the canonical copy.
type ClusterInput struct {
	Kind   string
	Name   string
	Copies []Copy
}

// Copy is one repo's version of an artifact body.
type Copy struct {
	Repo string
	Body string
}

// ClusterMerge is the LLM's reconciliation of a diverged cluster.
type ClusterMerge struct {
	CanonicalBody string
	Residues      []Residue // repo-specific guidance that must not go global
	Notes         string
	Usage         Usage
}

// Residue becomes a repo-scoped rule card.
type Residue struct {
	Repo    string
	Title   string
	Summary string
	Body    string
}

// ClaudeMDInput is one repo's root CLAUDE.md.
type ClaudeMDInput struct {
	Repo     string
	Filename string // defaults to CLAUDE.md; also supports AGENTS.md
	Content  string
}

// Decomposition splits a CLAUDE.md into atomic cards plus the residual
// repo-facts markdown that should stay in the repo.
type Decomposition struct {
	Cards    []DecomposedCard
	Residual string
	Notes    string
	Usage    Usage
}

// DecomposedCard is one atomic card extracted from a CLAUDE.md.
type DecomposedCard struct {
	Slug     string
	Type     string // rule | style | pattern
	Title    string
	Summary  string
	Keywords []string
	Scope    string // global | lang:<x> | repo:<name>
	Body     string
}

// Usage accumulates LLM token counts for cost reporting.
type Usage struct {
	Prompt     int
	Completion int
}

func (u *Usage) add(o Usage) { u.Prompt += o.Prompt; u.Completion += o.Completion }

// MergeResult reports what merge staged and what it could not.
type MergeResult struct {
	Staged  []string // paths relative to .import/staged/
	Skipped []string // human-readable reasons
	Notes   []string // LLM notes worth surfacing at review time
	Usage   Usage
}

// MergeOpts controls how Merge treats an existing staging area.
type MergeOpts struct {
	Force  bool // wipe staged output and progress, start over
	Resume bool // keep staged output, skip units already recorded as done
}

// Merge stages every cluster and CLAUDE.md decomposition into
// knowledge/.import/staged/. It never touches knowledge/ proper. An existing
// non-empty staging area is refused unless Force or Resume — it may hold
// un-applied review edits (contract C4). Progress is recorded per unit so an
// interrupted run resumes without repeating finished LLM calls.
func Merge(ctx context.Context, knowledgeDir string, rep Report, m Merger, opts MergeOpts) (MergeResult, error) {
	staged := filepath.Join(knowledgeDir, ".import", "staged")
	progress := progressPath(knowledgeDir)
	done := map[string]bool{}
	if opts.Resume {
		done = readProgress(progress)
	} else {
		if entries, err := os.ReadDir(staged); err == nil && len(entries) > 0 && !opts.Force {
			if len(entries) == 1 && entries[0].Name() == "residual" {
				return MergeResult{}, fmt.Errorf("importer: %s still holds residual CLAUDE.md files awaiting manual placement in their repos — place (then delete) them, or re-merge with --force to regenerate", staged)
			}
			return MergeResult{}, fmt.Errorf("importer: %s is not empty — `culi import apply` it, re-merge with --force, or continue an interrupted merge with --resume", staged)
		}
		if err := os.RemoveAll(staged); err != nil {
			return MergeResult{}, fmt.Errorf("importer: clearing staging: %w", err)
		}
		_ = os.Remove(progress)
	}
	var res MergeResult
	resumed := 0
	step := func(key string, run func() error) error {
		if done[key] {
			resumed++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("importer: merge interrupted: %w", err)
		}
		if err := run(); err != nil {
			return err
		}
		done[key] = true
		return writeProgress(progress, done)
	}
	for _, cl := range rep.Clusters {
		cl := cl
		if err := step("cluster:"+cl.Key, func() error {
			return mergeCluster(ctx, staged, cl, m, &res)
		}); err != nil {
			return res, err
		}
	}
	for _, it := range rep.ClaudeMD {
		it := it
		if err := step("claudemd:"+it.Repo, func() error {
			return mergeClaudeMD(ctx, staged, it, m, &res)
		}); err != nil {
			return res, err
		}
	}
	seenGuidance := map[string]bool{}
	for _, it := range rep.ClaudeMD {
		seenGuidance[it.NormHash] = true
	}
	for _, it := range rep.AgentsMD {
		it := it
		if seenGuidance[it.NormHash] {
			res.Skipped = append(res.Skipped, fmt.Sprintf("agentsmd/%s: identical guidance already imported", it.Repo))
			continue
		}
		if err := step("agentsmd:"+it.Repo, func() error {
			return mergeAgentsMD(ctx, staged, it, m, &res)
		}); err != nil {
			return res, err
		}
		seenGuidance[it.NormHash] = true
	}
	if resumed > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("resume: %d already-staged units skipped", resumed))
	}
	_ = os.Remove(progress) // complete — a later fresh merge must not skip
	return res, nil
}

// mergeCluster stages one cluster: mechanical copy for identical/superset/
// unique, LLM reconciliation for diverged.
func mergeCluster(ctx context.Context, staged string, cl Cluster, m Merger, res *MergeResult) error {
	canon := canonicalItem(cl)
	scope := []string{"global"}
	if cl.Class == "unique" {
		scope = []string{"repo:" + canon.Repo}
	}
	mergedFrom := make([]string, 0, len(cl.Items))
	for _, it := range cl.Items {
		mergedFrom = append(mergedFrom, it.Repo)
	}

	fmRaw, body, err := readArtifact(canon.Path)
	if err != nil {
		return err
	}
	model := ""
	if cl.Class == "diverged" {
		if m == nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: diverged — needs LLM merge (set ANTHROPIC_API_KEY)", cl.Key))
			return nil
		}
		in := ClusterInput{Kind: cl.Kind, Name: cl.Name}
		for _, it := range cl.Items {
			_, b, err := readArtifact(it.Path)
			if err != nil {
				return err
			}
			in.Copies = append(in.Copies, Copy{Repo: it.Repo, Body: b})
		}
		out, err := m.MergeCluster(ctx, in)
		if err != nil {
			return fmt.Errorf("importer: merging %s: %w", cl.Key, err)
		}
		res.Usage.add(out.Usage)
		if out.Notes != "" {
			res.Notes = append(res.Notes, cl.Key+": "+out.Notes)
		}
		body = out.CanonicalBody
		model = modelName(m)
		repos := map[string]bool{}
		for _, it := range cl.Items {
			repos[it.Repo] = true
		}
		for _, r := range out.Residues {
			// The LLM must not invent repo names: a residue for an unknown
			// repo falls back to the canonical copy's repo.
			if !repos[r.Repo] {
				res.Notes = append(res.Notes, fmt.Sprintf("%s: residue named unknown repo %q — rescoped to %s", cl.Key, r.Repo, canon.Repo))
				r.Repo = canon.Repo
			}
			rel, err := stageResidue(staged, cl, r)
			if err != nil {
				return err
			}
			res.Staged = append(res.Staged, rel)
		}
	}
	rel, err := stageArtifact(staged, cl, canon, fmRaw, body, scope, mergedFrom, model)
	if err != nil {
		return err
	}
	res.Staged = append(res.Staged, rel)
	if cl.AttachmentDrift {
		res.Notes = append(res.Notes, cl.Key+": attachment lists differ across repos — canonical copy's attachments staged; diff the rest manually")
	}
	return nil
}

// mergeClaudeMD decomposes one repo's CLAUDE.md into cards + residual.
func mergeClaudeMD(ctx context.Context, staged string, it Item, m Merger, res *MergeResult) error {
	return mergeGuidance(ctx, staged, it, m, res, "claudemd", "CLAUDE.md")
}

func mergeAgentsMD(ctx context.Context, staged string, it Item, m Merger, res *MergeResult) error {
	return mergeGuidance(ctx, staged, it, m, res, "agentsmd", filepath.Base(it.Path))
}

func mergeGuidance(ctx context.Context, staged string, it Item, m Merger, res *MergeResult, kind, filename string) error {
	if m == nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("%s/%s: needs LLM decomposition (set ANTHROPIC_API_KEY)", kind, it.Repo))
		return nil
	}
	raw, err := os.ReadFile(it.Path)
	if err != nil {
		return fmt.Errorf("importer: reading %s: %w", it.Path, err)
	}
	out, err := m.DecomposeClaudeMD(ctx, ClaudeMDInput{Repo: it.Repo, Filename: filename, Content: string(raw)})
	if err != nil {
		return fmt.Errorf("importer: decomposing %s of %s: %w", filename, it.Repo, err)
	}
	res.Usage.add(out.Usage)
	if out.Notes != "" {
		res.Notes = append(res.Notes, kind+"/"+it.Repo+": "+out.Notes)
	}
	for _, dc := range out.Cards {
		if kind == "agentsmd" {
			if it.Repo == "global" {
				dc.Scope = "global"
			} else {
				dc.Scope = "repo:" + it.Repo
			}
		}
		rel, err := stageDecomposed(staged, it.Repo, dc, modelName(m))
		if err != nil {
			return err
		}
		res.Staged = append(res.Staged, rel)
	}
	if strings.TrimSpace(out.Residual) != "" {
		rel := filepath.Join("residual", it.Repo+"."+filename)
		if err := writeStaged(staged, rel, []byte(out.Residual)); err != nil {
			return err
		}
		res.Staged = append(res.Staged, rel)
	}
	return nil
}

// canonicalItem resolves the cluster's canonical repo to its item.
func canonicalItem(cl Cluster) Item {
	for _, it := range cl.Items {
		if it.Repo == cl.Canonical {
			return it
		}
	}
	return cl.Items[0]
}

// readArtifact splits an agent/skill file into raw frontmatter and body.
func readArtifact(path string) (fmRaw, body string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("importer: reading %s: %w", path, err)
	}
	fm, b, err := knowledge.SplitFrontmatter(raw)
	if err != nil {
		return "", strings.TrimSpace(string(raw)), nil // unterminated fm: treat whole file as body
	}
	return string(fm), strings.TrimSpace(string(b)), nil
}

// modelName reports the merger's model for provenance, when it exposes one.
func modelName(m Merger) string {
	if n, ok := m.(interface{ ModelName() string }); ok {
		return n.ModelName()
	}
	return ""
}
