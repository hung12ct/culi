package cli

import (
	"context"
	"fmt"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/store"
)

// Down records an explicit downvote — the strongest negative utility signal.
// The multiplier is clamped, so even a hammered card stays retrievable;
// `culi card rm` is the way to actually remove one.
func Down(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("cli: usage: culi down <card-id>")
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
	c, err := s.CardByID(ctx, args[0])
	if err != nil {
		return err
	}
	if err := s.AddFeedback(ctx, c.ID, store.FeedbackDownvoted, 1); err != nil {
		return err
	}
	fmt.Printf("downvoted %s (%s) — it will rank lower, not disappear\n", c.ID, c.Title)
	return nil
}
