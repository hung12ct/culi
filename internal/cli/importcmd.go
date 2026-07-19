package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/importer"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// Import dispatches the drift-reconcile pipeline: scan → merge → apply.
func Import(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cli: usage: culi import scan|merge|apply")
	}
	switch args[0] {
	case "scan":
		return importScan(args[1:])
	case "merge":
		return importMerge(args[1:])
	case "apply":
		return importApply(args[1:])
	default:
		return fmt.Errorf("cli: unknown import subcommand %q (want scan|merge|apply)", args[0])
	}
}

// importScan inventories the repos read-only and writes scan.json.
func importScan(args []string) error {
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	repos := args
	if len(repos) == 0 {
		repos = cfg.Repos
	}
	rep, err := importer.Scan(repos)
	if err != nil {
		return err
	}
	path, err := importer.WriteReport(config.KnowledgeDir(base), rep)
	if err != nil {
		return err
	}
	printScan(rep)
	fmt.Printf("\nreport: %s\nnext:   culi import merge\n", path)
	return nil
}

// printScan renders the cluster report grouped by classification.
func printScan(rep importer.Report) {
	for _, r := range rep.Repos {
		cm := " "
		if r.ClaudeMD {
			cm = "+CLAUDE.md"
		}
		fmt.Printf("repo %-16s %2d agents %2d skills %s\n", r.Name, r.Agents, r.Skills, cm)
	}
	counts := map[string]int{}
	fmt.Println()
	for _, cl := range rep.Clusters {
		counts[cl.Class]++
		extra := ""
		if cl.Class == "diverged" {
			extra = fmt.Sprintf(" (worst overlap %.0f%%)", cl.Similarity*100)
		}
		if cl.Class == "superset" {
			extra = " (canonical: " + cl.Canonical + ")"
		}
		if cl.AttachmentDrift {
			extra += " [attachment drift]"
		}
		fmt.Printf("  %-9s %-28s ×%d%s\n", cl.Class, cl.Key, len(cl.Items), extra)
	}
	fmt.Printf("\nclusters: %d identical, %d superset, %d diverged, %d unique · %d CLAUDE.md\n",
		counts["identical"], counts["superset"], counts["diverged"], counts["unique"], len(rep.ClaudeMD))
}

// importMerge stages canonical cards. Diverged clusters and CLAUDE.md need a
// merge backend — resolved from config/flags, defaulting to whatever the user
// already has (API key, claude CLI subscription, or local Ollama).
func importMerge(args []string) error {
	fs := flag.NewFlagSet("import merge", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing non-empty staging area")
	noLLM := fs.Bool("no-llm", false, "mechanical merge only; skip diverged clusters and CLAUDE.md decomposition")
	provider := fs.String("provider", "", "merge backend: auto|anthropic|claude-cli|ollama|none (default: import.provider)")
	model := fs.String("model", "", "override import.merge_model")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	kdir := config.KnowledgeDir(base)
	rep, err := importer.ReadReport(kdir)
	if err != nil {
		return err
	}
	prov := cfg.Import.Provider
	if *provider != "" {
		prov = *provider
	}
	if *noLLM {
		prov = "none"
	}
	mm := cfg.Import.MergeModel
	if *model != "" {
		mm = *model
	}
	m, desc, err := resolveMerger(prov, mm, cfg.Ollama.Endpoint)
	if err != nil {
		return err
	}
	fmt.Printf("merge:  %s\n", desc)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res, err := importer.Merge(ctx, kdir, rep, m, *force)
	if err != nil {
		return err
	}
	for _, s := range res.Staged {
		fmt.Printf("staged  %s\n", s)
	}
	for _, s := range res.Skipped {
		fmt.Printf("skipped %s\n", s)
	}
	for _, n := range res.Notes {
		fmt.Printf("note    %s\n", n)
	}
	if res.Usage.Prompt+res.Usage.Completion > 0 {
		fmt.Printf("tokens  %d in / %d out\n", res.Usage.Prompt, res.Usage.Completion)
	}
	fmt.Printf("\nreview: git -C %s diff --no-index /dev/null .import/staged  (or open the files)\nnext:   culi import apply\n", kdir)
	return nil
}

// importApply moves reviewed staged files into knowledge/ and reindexes.
func importApply(args []string) error {
	fs := flag.NewFlagSet("import apply", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite conflicting existing cards")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, _, err := loadBase()
	if err != nil {
		return err
	}
	kdir := config.KnowledgeDir(base)
	res, err := importer.Apply(kdir, *force)
	if err != nil {
		return err
	}
	for _, p := range res.Applied {
		fmt.Printf("applied  %s\n", p)
	}
	for _, p := range res.Conflicts {
		fmt.Printf("conflict %s (kept staged; re-run with --force to overwrite)\n", p)
	}
	for _, p := range res.Residual {
		fmt.Printf("residual %s — replace that repo's CLAUDE.md with this by hand\n", p)
	}
	if len(res.Applied) > 0 {
		ctx := context.Background()
		s, err := store.Open(ctx, config.DBPath(base))
		if err != nil {
			return err
		}
		defer s.Close()
		sync, err := indexer.Sync(ctx, s, kdir)
		if err != nil {
			return err
		}
		fmt.Printf("\nindexed: %d upserted, %d skipped\n", sync.Upserted, len(sync.Skipped))
		// Governance trail, best-effort.
		_ = knowledge.Commit(kdir, fmt.Sprintf("import: applied %d reviewed cards", len(res.Applied)))
	}
	return nil
}

// resolveMerger picks the merge backend. "auto" prefers the strongest thing
// the user already has and never errors — worst case is a mechanical merge
// with a note listing the options. Explicit providers DO error when their
// prerequisite is missing (the user asked for that one specifically).
func resolveMerger(provider, model, ollamaEndpoint string) (importer.Merger, string, error) {
	switch provider {
	case "none":
		return nil, "mechanical only (diverged clusters and CLAUDE.md left for a later run)", nil
	case "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return nil, "", fmt.Errorf("cli: provider anthropic needs ANTHROPIC_API_KEY (or use --provider claude-cli / ollama)")
		}
		m, err := importer.NewLLMMerger(model)
		if err != nil {
			return nil, "", err
		}
		return m, model + " via Anthropic API", nil
	case "claude-cli":
		m, err := importer.NewCLIMerger(model)
		if err != nil {
			return nil, "", err
		}
		return m, model + " via claude CLI (uses your Claude subscription, no API key)", nil
	case "ollama":
		if strings.HasPrefix(model, "claude") {
			return nil, "", fmt.Errorf("cli: provider ollama needs a local generation model — set import.merge_model (e.g. qwen3), not %q", model)
		}
		return importer.NewOllamaMerger(ollamaEndpoint, model), model + " via local Ollama (free)", nil
	case "auto", "":
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			if m, err := importer.NewLLMMerger(model); err == nil {
				return m, model + " via Anthropic API (auto)", nil
			}
		}
		if m, err := importer.NewCLIMerger(model); err == nil {
			return m, model + " via claude CLI (auto — uses your Claude subscription)", nil
		}
		return nil, "mechanical only — no backend found. Options: set ANTHROPIC_API_KEY, " +
			"install the claude CLI, or set import.provider: ollama with a local model", nil
	default:
		return nil, "", fmt.Errorf("cli: unknown merge provider %q (want auto|anthropic|claude-cli|ollama|none)", provider)
	}
}

// loadBase resolves the culi home and its config in one step.
func loadBase() (string, config.Config, error) {
	base, err := config.BaseDir()
	if err != nil {
		return "", config.Config{}, err
	}
	cfg, err := config.Load(base)
	return base, cfg, err
}
