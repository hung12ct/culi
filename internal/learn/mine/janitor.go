package mine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// RetireStaleCandidates retires candidate cards left unreinforced for longer
// than ttl. The clock is the card file's mtime: every create, reinforce, or
// edit round-trips through knowledge.UpdateFile's atomic rename, so an
// untouched candidate keeps its birth mtime while a re-observed one resets it.
//
// Only candidates are eligible. Confirmed cards — including imported ones,
// which are also culi-authored — are NEVER auto-retired here; a dormant
// confirmed card is surfaced by `culi stats`, not deleted, because a wrongly
// deleted rule steers every future session (C4). Retirement itself is a
// reversible status flip on the file (RetireCard), committed to the knowledge
// git history by the caller. Returns the retired card IDs.
//
// Rejected alternative: an explicit `updated:` frontmatter field. File mtime
// already tracks exactly the mutations that count, at zero schema/render/
// backfill cost — and retirement being non-destructive absorbs mtime's one
// weakness (a git checkout that resets it merely restarts the clock).
func RetireStaleCandidates(ctx context.Context, s *store.Store, kdir string, ttl time.Duration, now time.Time) ([]string, error) {
	if ttl <= 0 {
		return nil, nil // disabled
	}
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("mine: listing cards for janitor: %w", err)
	}
	var retired []string
	for _, c := range cards {
		if c.Status != "candidate" {
			continue // fast filter on the index; the file is re-checked below
		}
		info, err := os.Stat(filepath.Join(kdir, filepath.FromSlash(c.Path)))
		if err != nil {
			continue // file gone or unreadable — skip, never fail the run
		}
		if now.Sub(info.ModTime()) <= ttl {
			continue
		}
		// Files are truth: confirm the on-disk status is still "candidate"
		// before retiring, so a stale index (DB says candidate, file is now
		// confirmed) can never let the janitor retire a confirmed card. Cheap
		// belt-and-braces on top of RetireCard's own culi-authorship guard.
		if fileCard, err := knowledge.ReadCard(kdir, c.Path); err != nil || fileCard.Status != "candidate" {
			continue
		}
		if ok, err := RetireCard(ctx, s, kdir, c.ID); err == nil && ok {
			retired = append(retired, c.ID)
		}
	}
	return retired, nil
}
