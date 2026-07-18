package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

// Index syncs knowledge/ into the SQLite index.
func Index(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	full := fs.Bool("full", false, "drop the index and rebuild from files")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
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

	var res indexer.Result
	if *full {
		res, err = indexer.Full(ctx, s, config.KnowledgeDir(base))
	} else {
		res, err = indexer.Sync(ctx, s, config.KnowledgeDir(base))
	}
	if err != nil {
		return err
	}
	fmt.Printf("indexed: %d upserted, %d deleted\n", res.Upserted, res.Deleted)
	for _, skip := range res.Skipped {
		fmt.Printf("skipped: %s (parse error)\n", skip)
	}
	return nil
}
