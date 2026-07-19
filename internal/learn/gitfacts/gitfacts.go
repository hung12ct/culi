// Package gitfacts renders a deterministic, zero-LLM analysis of a git
// repository: modules and churn, commit conventions, dependencies, build/test
// layout, branch-unique work, and the existing CLAUDE.md as a prior. Its
// output is the entire input to `culi gen`'s model calls — pay for
// intelligence only where determinism can't reach (§9). The render is
// content-hashed so an unchanged repo costs zero LLM calls.
package gitfacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

// Collection caps — the render must stay in the 3-8KB band the plan budgets
// for one Strong call, regardless of repo size.
const (
	churnCommits  = 500
	subjectSample = 100
	maxModules    = 12
	maxDeps       = 20
	maxBranchLog  = 40
	maxPriorChars = 4000
)

// Facts is one repo's deterministic analysis.
type Facts struct {
	Repo    string
	Root    string
	Branch  string // current branch ("" when detached/unreadable)
	Modules []Module
	// Conventions describes commit-subject style (conventional-commit ratio,
	// common prefixes).
	Conventions string
	Deps        []string
	BuildTest   string // Makefile targets, CI workflows, test-file count
	// BranchWork is non-empty only when a branch was requested: unique
	// commits vs the merge base and the directories they touch.
	BranchWork string
	// Prior is the existing CLAUDE.md with culi marker spans stripped — the
	// human-authored content the draft must respect, never duplicate.
	Prior string
}

// Collect analyzes the repo at root. branch "" analyzes the repo as a whole;
// otherwise BranchWork covers branch-unique commits. Individual probe
// failures degrade to empty sections (detached HEAD, mid-rebase, shallow
// clones must not fail the run); only "not a git repo" is fatal.
func Collect(ctx context.Context, root, branch string) (Facts, error) {
	root = filepath.Clean(root)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return Facts{}, fmt.Errorf("gitfacts: %s is not a git repository root: %w", root, err)
	}
	f := Facts{Repo: filepath.Base(root), Root: root}

	// Independent probes fan out (14.26); each swallows its own failure.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { f.Branch = currentBranch(gctx, root); return nil })
	g.Go(func() error { f.Modules = modules(gctx, root); return nil })
	g.Go(func() error { f.Conventions = conventions(gctx, root); return nil })
	g.Go(func() error { f.Deps = deps(root); return nil })
	g.Go(func() error { f.BuildTest = buildTest(root); return nil })
	g.Go(func() error { f.Prior = prior(root); return nil })
	if branch != "" {
		g.Go(func() error { f.BranchWork = branchWork(gctx, root, branch); return nil })
	}
	if err := g.Wait(); err != nil {
		return f, fmt.Errorf("gitfacts: %w", err)
	}
	return f, nil
}

// Render renders the facts as the markdown fed to the model.
func (f Facts) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Repository facts: %s\n\n", f.Repo)
	if f.Branch != "" {
		fmt.Fprintf(&b, "Current branch: %s\n\n", f.Branch)
	}
	if len(f.Modules) > 0 {
		b.WriteString("## Modules by churn (commits touching, last 500 commits)\n\n")
		for _, m := range f.Modules {
			fmt.Fprintf(&b, "- %s — %d commits, ~%d lines changed\n", m.Dir, m.Commits, m.Lines)
		}
		b.WriteString("\n")
	}
	writeSection(&b, "Commit conventions", f.Conventions)
	if len(f.Deps) > 0 {
		b.WriteString("## Direct dependencies\n\n" + strings.Join(f.Deps, ", ") + "\n\n")
	}
	writeSection(&b, "Build and test layout", f.BuildTest)
	writeSection(&b, "Branch-unique work", f.BranchWork)
	writeSection(&b, "Existing CLAUDE.md (human-authored prior — do not duplicate)", f.Prior)
	return b.String()
}

// Hash fingerprints the render; unchanged hash = whole gen run is a no-op.
func (f Facts) Hash() string {
	sum := sha256.Sum256([]byte(f.Render()))
	return hex.EncodeToString(sum[:8])
}

func writeSection(b *strings.Builder, title, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	b.WriteString("## " + title + "\n\n" + strings.TrimSpace(body) + "\n\n")
}

// git runs one git subcommand with the repo as workdir, returning "" on any
// failure — probes degrade, never fail (mid-rebase, detached HEAD, no
// upstream are all normal states).
func git(ctx context.Context, root string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return ""
	}
	return out.String()
}

func currentBranch(ctx context.Context, root string) string {
	b := strings.TrimSpace(git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"))
	if b == "HEAD" { // detached
		return ""
	}
	return b
}
