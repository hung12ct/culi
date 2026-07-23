// Package branchgen implements learning pipeline C (`culi gen`): repository
// git facts → two one-shot structured calls (Strong drafts CLAUDE.md
// sections, Cheap extracts repo-scoped cards) → idempotent writes. The whole
// run is a no-op when the facts hash is unchanged (zero LLM calls), and
// CLAUDE.md regeneration touches only culi marker spans (C4).
package branchgen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/gitfacts"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

// Generator wires pipeline C's dependencies.
type Generator struct {
	Base   string // culi home
	Store  *store.Store
	Tier   *llmtier.Tier
	Logf   func(format string, args ...any)
	Target string // claude | codex | both; empty preserves claude
}

// Result reports one gen run.
type Result struct {
	NoOp         bool     // facts unchanged — nothing ran
	Sections     []string // CLAUDE.md span ids written/updated
	Conflicts    []string // spans the user edited — left untouched
	CardsWritten []string
	CardsSkipped []string // unchanged hash or hand-authored path
	Notes        []string
	Usage        llmgen.Usage
}

// Generate runs pipeline C for already-collected facts. branch "" = repo
// mode (CLAUDE.md sections + repo-scoped cards); branch mode extracts
// branch-scoped cards only — CLAUDE.md stays a repo-level artifact.
func (g *Generator) Generate(ctx context.Context, facts gitfacts.Facts, branch string, force bool) (Result, error) {
	var res Result
	target, err := normalizeTarget(g.Target)
	if err != nil {
		return res, err
	}
	stateDir := config.StateDir(g.Base)
	key := facts.Repo
	if branch != "" {
		key += "@" + branch
	}
	// Keyed by root path, not basename — two checkouts named alike must not
	// share no-op state.
	key = facts.Root + "|" + key + "|target:" + target
	st := loadGenState(stateDir)
	hash := facts.Hash()
	if !force && st[key] == hash {
		res.NoOp = true
		return res, nil
	}
	factsMD := facts.Render()

	if branch == "" {
		if err := g.genSections(ctx, facts.Root, target, factsMD, &res); err != nil {
			return res, err
		}
	}
	if err := g.genCards(ctx, facts.Repo, branch, hash, factsMD, &res); err != nil {
		return res, err
	}
	if _, err := indexer.Sync(ctx, g.Store, config.KnowledgeDir(g.Base)); err != nil {
		return res, fmt.Errorf("branchgen: %w", err)
	}
	if len(res.CardsWritten) > 0 {
		msg := fmt.Sprintf("gen: %s: %d cards from git facts %s", facts.Repo, len(res.CardsWritten), hash)
		if err := knowledge.Commit(config.KnowledgeDir(g.Base), msg); err != nil {
			g.logf("gen: knowledge commit: %v", err) // best-effort
		}
	}

	st[key] = hash
	if err := saveGenState(stateDir, st); err != nil {
		res.Notes = append(res.Notes, err.Error())
	}
	return res, nil
}

// genSections drafts and merges the CLAUDE.md spans (Strong tier — §9).
func (g *Generator) genSections(ctx context.Context, root, target, factsMD string, res *Result) error {
	var out sectionsOut
	usage, err := g.Tier.Generate(ctx, true, sectionsSystem, factsMD, "claudemd_sections", sectionsSchema(), &out)
	res.Usage.Add(usage)
	if err != nil {
		return fmt.Errorf("branchgen: %w", err)
	}
	if out.Notes != "" {
		res.Notes = append(res.Notes, "sections: "+out.Notes)
	}
	secs := make([]Section, 0, len(out.Sections))
	for _, s := range out.Sections {
		if len(secs) == maxSections {
			break
		}
		secs = append(secs, Section{ID: slugify(s.ID), Title: s.Title, Markdown: s.Markdown})
	}

	files := []string{"CLAUDE.md"}
	switch target {
	case "codex":
		files = []string{"AGENTS.md"}
	case "both":
		files = []string{"CLAUDE.md", "AGENTS.md"}
	}
	for _, filename := range files {
		path := filepath.Join(root, filename)
		existing := ""
		if raw, err := os.ReadFile(path); err == nil {
			existing = string(raw)
		}
		merged, applied, conflicts := mergeClaudeMD(existing, secs)
		for _, id := range applied {
			if len(files) > 1 || filename != "CLAUDE.md" {
				id = filename + ":" + id
			}
			res.Sections = append(res.Sections, id)
		}
		for _, id := range conflicts {
			if len(files) > 1 || filename != "CLAUDE.md" {
				id = filename + ":" + id
			}
			res.Conflicts = append(res.Conflicts, id)
		}
		if merged == existing {
			continue
		}
		if err := writeInstructionFile(path, merged); err != nil {
			return err
		}
		g.logf("%s: %d span(s) written, %d conflict(s)", filename, len(applied), len(conflicts))
	}
	return nil
}

func normalizeTarget(target string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "claude":
		return "claude", nil
	case "codex":
		return "codex", nil
	case "both":
		return "both", nil
	default:
		return "", fmt.Errorf("branchgen: unknown target %q (want claude|codex|both)", target)
	}
}

// genCards extracts and upserts repo/branch-scoped cards (Cheap tier, one
// escalation on failure — §9).
func (g *Generator) genCards(ctx context.Context, repo, branch, factsHash, factsMD string, res *Result) error {
	system := fmt.Sprintf(cardsSystem, maxCards, minConfidence)
	var out cardsOut
	usage, err := g.Tier.Generate(ctx, false, system, factsMD, "repo_cards", cardsSchema(), &out)
	res.Usage.Add(usage)
	if err != nil {
		if llmtier.IsStop(err) {
			return fmt.Errorf("branchgen: %w", err) // capped or backend down — skip strong-tier retry
		}
		out = cardsOut{}
		usage, err = g.Tier.Generate(ctx, true, system, factsMD, "repo_cards", cardsSchema(), &out)
		res.Usage.Add(usage)
		if err != nil {
			return fmt.Errorf("branchgen: %w", err)
		}
	}
	if out.Notes != "" {
		res.Notes = append(res.Notes, "cards: "+out.Notes)
	}
	if len(out.Cards) > maxCards {
		out.Cards = out.Cards[:maxCards]
	}
	for _, c := range out.Cards {
		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Markdown) == "" ||
			c.Confidence < minConfidence {
			continue
		}
		id, wrote, note, err := g.upsertCard(repo, branch, factsHash, c)
		if err != nil {
			return err
		}
		switch {
		case wrote:
			res.CardsWritten = append(res.CardsWritten, id)
		case note != "":
			res.CardsSkipped = append(res.CardsSkipped, note)
		}
	}
	return nil
}

func (g *Generator) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}
