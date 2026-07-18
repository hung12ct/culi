package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/export"
)

// Export regenerates ~/.claude/agents and ~/.claude/skills from canonical
// cards. --check reports drift (hand edits, pending updates) without writing.
func Export(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	check := fs.Bool("check", false, "report what would change without writing")
	force := fs.Bool("force", false, "overwrite hand-edited generated files")
	dir := fs.String("dir", "", "target directory (default ~/.claude)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, _, err := loadBase()
	if err != nil {
		return err
	}
	target := *dir
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cli: resolving home: %w", err)
		}
		target = filepath.Join(home, ".claude")
	}
	statePath := filepath.Join(config.StateDir(base), "export-manifest.json")
	res, err := export.Run(config.KnowledgeDir(base), target, statePath, *check, *force)
	if err != nil {
		return err
	}
	verb := "wrote"
	if *check {
		verb = "would write"
	}
	for _, p := range res.Written {
		fmt.Printf("%-11s %s\n", verb, p)
	}
	verb = "removed"
	if *check {
		verb = "would remove"
	}
	for _, p := range res.Removed {
		fmt.Printf("%-11s %s (card gone)\n", verb, p)
	}
	for _, p := range res.Edited {
		fmt.Printf("hand-edited %s — kept; fold changes into the card or re-run with --force\n", p)
	}
	for _, p := range res.Skipped {
		fmt.Printf("kept-local  %s\n", p)
	}
	if *check {
		if res.Drifted() {
			return fmt.Errorf("cli: export drift detected")
		}
		fmt.Println("in sync")
		return nil
	}
	if len(res.Written)+len(res.Removed) == 0 {
		fmt.Println("in sync — nothing to do")
	}
	return nil
}
