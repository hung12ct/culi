package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/pack"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// Query runs the retrieval funnel from the terminal — the debug window into
// what a hook would inject for a given prompt.
func Query(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	timing := fs.Bool("timing", false, "print per-stage latency")
	cwd := fs.String("cwd", "", "resolve scope as if running from this directory (default: current)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return fmt.Errorf("cli: usage: culi query [--timing] [--cwd dir] <prompt>")
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cli: getwd: %w", err)
		}
		*cwd = wd
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

	t0 := time.Now()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()
	tOpen := time.Since(t0)

	retrieve.ExtendLexicon(cfg.ExtraAcks, cfg.ExtraStopwords)
	gate := retrieve.Gate(prompt, "")
	if gate.Skip {
		fmt.Printf("gate: SKIP (%s) — a hook would inject nothing\n", gate.Reason)
		return nil
	}

	t1 := time.Now()
	sc := retrieve.DetectScope(*cwd)
	tScope := time.Since(t1)

	t2 := time.Now()
	r := &retrieve.Retriever{Store: s}
	cands, err := r.Retrieve(ctx, gate.Query, sc)
	if err != nil {
		return err
	}
	tRetrieve := time.Since(t2)

	t3 := time.Now()
	inj, err := pack.Pack(cands, nil, cfg.PushBudget, func(rowids []int64) (map[int64]string, error) {
		cards, err := s.CardsByRowid(ctx, rowids)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]string, len(cards))
		for _, c := range cards {
			out[c.Rowid] = c.Body
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	tPack := time.Since(t3)

	fmt.Printf("scope: repo=%q branch=%q langs=%v\n", sc.Repo, sc.Branch, sc.Langs)
	if len(cands) == 0 {
		fmt.Println("result: nothing above the score floor — a hook would inject nothing")
	} else {
		fmt.Printf("candidates (%d):\n", len(cands))
		for _, c := range cands {
			pin := "     "
			if c.Pinned {
				pin = "[pin]"
			}
			fmt.Printf("  %s %.5f  %-30s %s\n", pin, c.Score, c.Card.ID, c.Card.Title)
		}
		fmt.Printf("\ninjection (%d tokens):\n%s\n", inj.Tokens, inj.Render())
	}
	if *timing {
		fmt.Printf("\ntiming: open=%s scope=%s retrieve=%s pack=%s total=%s\n",
			tOpen, tScope, tRetrieve, tPack, time.Since(t0))
	}
	return nil
}
