package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/learn/branchgen"
	"github.com/hung12ct/culi/internal/learn/gitfacts"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/store"
)

// genTimeout bounds the whole run: facts collection plus at most two model
// calls.
const genTimeout = 10 * time.Minute

// Gen runs learning pipeline C: git facts → CLAUDE.md marker spans +
// repo-scoped cards. Explicit user command, so unlike the background worker
// it errors loudly when no backend is available.
func Gen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "repository path or configured repo name (default: current directory's repo)")
	branch := fs.String("branch", "", "extract branch-scoped cards for this branch instead of regenerating CLAUDE.md")
	dryRun := fs.Bool("dry-run", false, "print the rendered git facts and exit (zero LLM calls)")
	force := fs.Bool("force", false, "regenerate even when the facts hash is unchanged")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	root, err := resolveRepo(*repoFlag, cfg.Repos)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), genTimeout)
	defer cancel()
	facts, err := gitfacts.Collect(ctx, root, *branch)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("facts hash: %s\n\n%s", facts.Hash(), facts.Render())
		return nil
	}

	tier, desc, err := llmtier.Resolve(cfg.Learn, cfg.Ollama.Endpoint, config.StateDir(base))
	if err != nil {
		return err
	}
	if tier == nil {
		return fmt.Errorf("cli: gen needs a model backend — %s", desc)
	}
	fmt.Printf("repo:    %s\nbackend: %s\n", root, desc)

	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()
	g := &branchgen.Generator{Base: base, Store: s, Tier: tier}
	res, err := g.Generate(ctx, facts, *branch, *force)
	printGen(res)
	return err
}

func printGen(res branchgen.Result) {
	if res.NoOp {
		fmt.Println("no-op: git facts unchanged since the last run (--force to regenerate)")
		return
	}
	for _, s := range res.Sections {
		fmt.Printf("section  %s\n", s)
	}
	for _, c := range res.Conflicts {
		fmt.Printf("conflict %s — you edited inside this span; left untouched (delete the span to regenerate it)\n", c)
	}
	for _, c := range res.CardsWritten {
		fmt.Printf("card     %s\n", c)
	}
	for _, c := range res.CardsSkipped {
		fmt.Printf("skipped  %s\n", c)
	}
	for _, n := range res.Notes {
		fmt.Printf("note     %s\n", n)
	}
	if res.Usage.Prompt+res.Usage.Completion > 0 {
		fmt.Printf("tokens   %d in / %d out\n", res.Usage.Prompt, res.Usage.Completion)
	}
}

// resolveRepo maps the --repo flag to a repo root: an absolute/relative path,
// a configured repo's basename, or (empty) the git root above the cwd.
func resolveRepo(flagVal string, repos []string) (string, error) {
	if flagVal == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cli: %w", err)
		}
		if root := findGitRootDir(cwd); root != "" {
			return root, nil
		}
		return "", fmt.Errorf("cli: %s is not inside a git repository — pass --repo", cwd)
	}
	if st, err := os.Stat(flagVal); err == nil && st.IsDir() {
		abs, err := filepath.Abs(flagVal)
		if err != nil {
			return "", fmt.Errorf("cli: %w", err)
		}
		return abs, nil
	}
	for _, r := range repos {
		if filepath.Base(r) == flagVal {
			return r, nil
		}
	}
	return "", fmt.Errorf("cli: repo %q is neither a directory nor a configured repo name", flagVal)
}

// findGitRootDir walks up from dir looking for .git.
func findGitRootDir(dir string) string {
	for range 24 {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}
