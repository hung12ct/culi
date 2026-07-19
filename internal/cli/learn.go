package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/learn"
)

// learnTimeout bounds one worker run: a wedged backend must not leave a
// zombie holding the lock forever (the lock's staleness guard is the backstop).
const learnTimeout = 15 * time.Minute

// Learn drains the learning inbox once. --auto is the detached mode the
// session-end hook spawns: all output goes to logs/learn.log, exit is always
// clean (a background learner must never surface errors into a terminal that
// isn't there).
func Learn(args []string) error {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fromStart := fs.Bool("from-start", false, "ignore cursors and re-mine transcripts from the beginning")
	auto := fs.Bool("auto", false, "background mode: log to ~/.culi/logs/learn.log (used by the session-end hook)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}

	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	if *auto {
		logf = fileLogf(base, "learn.log")
	}

	ctx, cancel := context.WithTimeout(context.Background(), learnTimeout)
	defer cancel()
	sum, err := learn.Run(ctx, base, cfg, *fromStart, logf)
	if err != nil {
		if *auto {
			logf("learn: %v", err)
			return nil
		}
		return err
	}

	if sum.Backend != "" {
		logf("backend: %s", sum.Backend)
	}
	logf("jobs: %d (%d mined, %d clean)", sum.Jobs, sum.Mined, sum.Clean)
	for _, id := range sum.Created {
		logf("candidate  %s", id)
	}
	for _, id := range sum.Reinforced {
		logf("reinforced %s", id)
	}
	for _, id := range sum.Confirmed {
		logf("confirmed  %s", id)
	}
	for _, id := range sum.Retired {
		logf("retired    %s (superseded)", id)
	}
	if sum.StyleObs > 0 {
		logf("style observations: +%d (synthesized periodically)", sum.StyleObs)
	}
	for _, n := range sum.Notes {
		logf("note: %s", n)
	}
	if sum.Usage.Prompt+sum.Usage.Completion > 0 {
		logf("tokens: %d in / %d out", sum.Usage.Prompt, sum.Usage.Completion)
	}
	if len(sum.Created) > 0 {
		logf("review candidates with: culi review")
	}
	return nil
}

// fileLogf appends timestamped lines to logs/<name>; diagnostics only, so
// every failure path is silent.
func fileLogf(base, name string) func(string, ...any) {
	return func(format string, args ...any) {
		dir := config.LogDir(base)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), strings.TrimRight(msg, "\n"))
	}
}
