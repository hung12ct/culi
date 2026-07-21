// Package patterns implements learning pipeline D: an offline cross-branch
// pattern index. The learn worker compares each configured repo's branch tips
// against persisted state (milliseconds of git, zero LLM); only branches
// whose tips moved are re-mined — commit clusters become work units, one
// cheap call per branch turns them into repo-scoped pattern cards whose
// bodies carry representative diff hunks (the payload that makes "apply this
// pattern" work at retrieval time). Deleted branches retire their cards
// unless the work was merged.
package patterns

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

// maxBranchMines caps model calls per worker run — a repo with many moved
// branches drains over successive runs instead of bursting the ledger.
const maxBranchMines = 4

// Runner wires pipeline D's dependencies.
type Runner struct {
	Base  string
	Store *store.Store
	Tier  *llmtier.Tier
	Logf  func(format string, args ...any)
}

// Result reports one pattern-index run.
type Result struct {
	Branches int // branches mined this run
	Created  []string
	Skipped  []string
	Retired  []string // cards of deleted, unmerged branches
	Notes    []string
	Usage    llmgen.Usage
}

// Run indexes every configured repo. Per-repo failures degrade to notes —
// one broken checkout must not stall the others. ErrCapped stops the run
// with unmined tips left un-advanced (re-mined next run).
func (r *Runner) Run(ctx context.Context, repos []string) (Result, error) {
	var res Result
	if len(repos) == 0 {
		return res, nil
	}
	stateDir := config.StateDir(r.Base)
	tips := loadTips(stateDir)
	changed := false

	for _, root := range repos {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			continue // not checked out here; skip silently
		}
		capped, err := r.indexRepo(ctx, root, tips, &res)
		if err != nil {
			res.Notes = append(res.Notes, filepath.Base(root)+": "+err.Error())
		}
		changed = true
		if capped {
			res.Notes = append(res.Notes, "pattern mining stopped at the daily cap — remaining branches next run")
			break
		}
	}
	if changed {
		if err := saveTips(stateDir, tips); err != nil {
			res.Notes = append(res.Notes, err.Error())
		}
	}
	if len(res.Created) > 0 || len(res.Retired) > 0 {
		if _, err := indexer.Sync(ctx, r.Store, config.KnowledgeDir(r.Base)); err != nil {
			return res, fmt.Errorf("patterns: reindexing: %w", err)
		}
	}
	return res, nil
}

// indexRepo diffs one repo's branch tips against state and mines movers.
func (r *Runner) indexRepo(ctx context.Context, root string, tips tipState, res *Result) (capped bool, err error) {
	cur, err := branchTips(ctx, root)
	if err != nil {
		return false, err
	}
	base := defaultBranch(ctx, root)
	prev := tips[root]
	if prev == nil {
		prev = map[string]string{}
		tips[root] = prev
	}

	// Deleted branches: retire their cards unless the recorded tip merged.
	for branch, sha := range prev {
		if _, ok := cur[branch]; ok {
			continue
		}
		delete(prev, branch)
		if base != "" && isAncestor(ctx, root, sha, base) {
			continue // work landed on the default branch: patterns stay valid
		}
		res.Retired = append(res.Retired, r.retireBranchCards(root, branch)...)
	}

	// Moved/new branches, deterministic order; the default branch itself has
	// no merge-base window and is skipped.
	branches := make([]string, 0, len(cur))
	for b := range cur {
		if b != base && prev[b] != cur[b] {
			branches = append(branches, b)
		}
	}
	sort.Strings(branches)
	for _, branch := range branches {
		if res.Branches >= maxBranchMines {
			return false, nil // quietly resume next run
		}
		mined, err := r.mineBranch(ctx, root, base, branch, cur[branch], res)
		if llmtier.IsStop(err) {
			return true, nil // capped or backend down — stop, resume next run
		}
		if err != nil {
			res.Notes = append(res.Notes, branch+": "+err.Error())
			continue // tip NOT advanced: retried next run
		}
		if mined {
			res.Branches++
		}
		prev[branch] = cur[branch]
	}
	return false, nil
}

// tipState maps repo root → branch → last-indexed sha.
type tipState map[string]map[string]string

func tipsPath(stateDir string) string { return filepath.Join(stateDir, "branch_tips.json") }

func loadTips(stateDir string) tipState {
	st := tipState{}
	raw, err := os.ReadFile(tipsPath(stateDir))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	if st == nil {
		st = tipState{}
	}
	return st
}

func saveTips(stateDir string, st tipState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("patterns: marshaling tips: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("patterns: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, ".tips-*")
	if err != nil {
		return fmt.Errorf("patterns: creating temp tips: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("patterns: writing tips: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("patterns: closing tips: %w", err)
	}
	if err := os.Rename(name, tipsPath(stateDir)); err != nil {
		os.Remove(name)
		return fmt.Errorf("patterns: replacing tips: %w", err)
	}
	return nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return strings.TrimSpace(s)
}
