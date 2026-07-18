package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

// Card inspects the card store: list, show <id>, rm <id>.
func Card(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("cli: usage: culi card list|show|rm <id>")
	}
	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	ctx := context.Background()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()

	switch args[0] {
	case "list":
		return cardList(ctx, s)
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("cli: usage: culi card show <id>")
		}
		return cardShow(ctx, s, base, args[1])
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("cli: usage: culi card rm <id>")
		}
		return cardRm(ctx, s, base, args[1])
	default:
		return fmt.Errorf("cli: unknown card subcommand %q", args[0])
	}
}

func cardList(ctx context.Context, s *store.Store) error {
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return err
	}
	if len(cards) == 0 {
		fmt.Println("no cards — seed knowledge/ and run `culi index`")
		return nil
	}
	fmt.Printf("%-6s %-8s %-34s %-24s %s\n", "SHORT", "TYPE", "ID", "SCOPES", "TITLE")
	for _, c := range cards {
		fmt.Printf("%-6s %-8s %-34s %-24s %s\n",
			c.ShortID, c.Type, c.ID, strings.Join(c.Scopes, ","), c.Title)
	}
	return nil
}

func cardShow(ctx context.Context, s *store.Store, base, id string) error {
	c, err := s.CardByID(ctx, id)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(config.KnowledgeDir(base), filepath.FromSlash(c.Path)))
	if err != nil {
		return fmt.Errorf("cli: reading %s: %w", c.Path, err)
	}
	fmt.Printf("# %s (%s, %d body tokens)\n\n%s", c.Path, c.ShortID, c.TokBody, raw)
	return nil
}

func cardRm(ctx context.Context, s *store.Store, base, id string) error {
	c, err := s.CardByID(ctx, id)
	if err != nil {
		return err
	}
	full := filepath.Join(config.KnowledgeDir(base), filepath.FromSlash(c.Path))
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("cli: removing %s: %w", c.Path, err)
	}
	if _, err := indexer.Sync(ctx, s, config.KnowledgeDir(base)); err != nil {
		return err
	}
	fmt.Printf("removed %s (%s)\n", c.ID, c.Path)
	return nil
}
