package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

// Index syncs knowledge/ into the SQLite index, then embeds cards missing a
// fresh vector. A dead Ollama downgrades the second step to a notice — BM25
// keeps working regardless.
func Index(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	full := fs.Bool("full", false, "rebuild card search from files (preserves activity history)")
	noEmbed := fs.Bool("no-embed", false, "skip the embedding pass")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	base, err := config.BaseDir()
	if err != nil {
		return err
	}
	cfg, err := config.Load(base)
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

	if *noEmbed {
		return nil
	}
	e := embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model)
	n, err := indexer.EmbedMissing(ctx, s, e, cfg.Ollama.Model)
	if err != nil {
		fmt.Printf("embeddings: %d done, then skipped — %v (BM25-only retrieval until Ollama is back)\n", n, err)
		return nil
	}
	fmt.Printf("embeddings: %d computed (%s)\n", n, cfg.Ollama.Model)
	return nil
}
