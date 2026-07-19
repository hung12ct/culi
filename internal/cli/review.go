package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/mine"
	"github.com/hung12ct/culi/internal/store"
)

// Review is the optional HITL gate over mined candidate cards: approve
// (confirm — starts injecting), reject (retire), or leave for later. Mining
// never blocks on review; confirmation also happens organically at two
// observations.
func Review(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	list := fs.Bool("list", false, "list candidates and exit")
	approve := fs.String("approve", "", "approve one candidate by ID")
	reject := fs.String("reject", "", "reject one candidate by ID")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, _, err := loadBase()
	if err != nil {
		return err
	}
	kdir := config.KnowledgeDir(base)
	ctx := context.Background()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := indexer.Sync(ctx, s, kdir); err != nil {
		return err
	}

	if *approve != "" || *reject != "" {
		if *approve != "" {
			if err := approveOne(ctx, s, kdir, *approve); err != nil {
				return err
			}
		}
		if *reject != "" {
			if err := mine.Reject(ctx, s, kdir, *reject); err != nil {
				return err
			}
			fmt.Printf("rejected %s (retired)\n", *reject)
		}
		_, err := indexer.Sync(ctx, s, kdir)
		return err
	}

	cands, err := candidates(ctx, s)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		fmt.Println("no candidate cards — nothing to review")
		return nil
	}
	if *list {
		for _, c := range cands {
			fmt.Printf("%-9s %-40s %s\n", c.Type, c.ID, c.Title)
		}
		fmt.Printf("\n%d candidate(s). Approve/reject: culi review [--approve <id>|--reject <id>]\n", len(cands))
		return nil
	}

	return interactiveReview(ctx, s, kdir, cands)
}

// candidates returns candidate-status cards, newest first.
func candidates(ctx context.Context, s *store.Store) ([]store.StoredCard, error) {
	all, err := s.AllCardsMeta(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.StoredCard
	for _, c := range all {
		if c.Status == "candidate" {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Rowid > out[b].Rowid })
	return out, nil
}

// interactiveReview walks candidates one by one on stdin.
func interactiveReview(ctx context.Context, s *store.Store, kdir string, cands []store.StoredCard) error {
	sc := bufio.NewScanner(os.Stdin)
	acted := false
	for i, c := range cands {
		file, err := knowledge.ReadCard(kdir, c.Path)
		if err != nil {
			fmt.Printf("skipping %s: %v\n", c.ID, err)
			continue
		}
		fmt.Printf("\n[%d/%d] %s  %s\n", i+1, len(cands), c.Type, c.ID)
		fmt.Printf("  title:   %s\n  summary: %s\n  scope:   %s  observations: %d\n",
			file.Title, file.Summary, strings.Join(file.Scopes, ", "), file.Observations)
		if file.Supersedes != "" {
			fmt.Printf("  supersedes: %s (retired on approval)\n", file.Supersedes)
		}
		fmt.Print("  [a]pprove / [r]eject / [s]kip / [b]ody / [q]uit: ")
		for {
			if !sc.Scan() {
				return finishReview(ctx, s, kdir, acted)
			}
			switch strings.ToLower(strings.TrimSpace(sc.Text())) {
			case "a":
				if err := approveOne(ctx, s, kdir, c.ID); err != nil {
					return err
				}
				acted = true
			case "r":
				if err := mine.Reject(ctx, s, kdir, c.ID); err != nil {
					return err
				}
				fmt.Printf("rejected %s (retired)\n", c.ID)
				acted = true
			case "s", "":
			case "b":
				fmt.Printf("\n%s\n\n  [a]pprove / [r]eject / [s]kip / [b]ody / [q]uit: ", file.Body)
				continue
			case "q":
				return finishReview(ctx, s, kdir, acted)
			default:
				fmt.Print("  a/r/s/b/q: ")
				continue
			}
			break
		}
	}
	return finishReview(ctx, s, kdir, acted)
}

func approveOne(ctx context.Context, s *store.Store, kdir, id string) error {
	retired, err := mine.Confirm(ctx, s, kdir, id)
	if err != nil {
		return err
	}
	fmt.Printf("approved %s (confirmed — will inject)\n", id)
	if retired != "" {
		fmt.Printf("retired  %s (superseded)\n", retired)
	}
	return nil
}

func finishReview(ctx context.Context, s *store.Store, kdir string, acted bool) error {
	if !acted {
		return nil
	}
	_, err := indexer.Sync(ctx, s, kdir)
	return err
}
