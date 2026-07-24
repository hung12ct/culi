package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/serve"
	"github.com/hung12ct/culi/internal/store"
)

// Serve runs the local review console (plan §5): a web UI over the knowledge
// store for candidate triage, KB browsing, and token-savings/job-health
// dashboards. It is off every hot path — a convenience surface, not part of
// hook or MCP serving — so it opens the store read-mostly and reuses the same
// mine/index/knowledge functions `culi review` does for its few writes.
//
// The StatsFn closure hands the console the `culi stats` report without serve
// importing this package (which would cycle): serve re-marshals it to JSON.
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "localhost:7378", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()

	return serve.Run(ctx, serve.Options{
		Base:    base,
		Cfg:     cfg,
		Store:   s,
		Addr:    *addr,
		Version: versionLabel(),
		StatsFn: func(c context.Context) any { return gather(c, s, base) },
	})
}
