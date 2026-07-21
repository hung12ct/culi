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
	forceStyle := fs.Bool("style", false, "force style synthesis now (bypass the weekly/15-observation trigger)")
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
	sum, err := learn.Run(ctx, base, cfg, learn.Options{FromStart: *fromStart, ForceStyle: *forceStyle}, logf)
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
	if sum.BackendDown {
		logf("learning halted: backend unavailable — jobs kept queued, cap untouched. " +
			"claude CLI: run `claude auth login`; API: set ANTHROPIC_API_KEY.")
	}
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
	if sum.Patterns.Branches > 0 || len(sum.Patterns.Retired) > 0 {
		logf("pattern index: %d branch(es) mined", sum.Patterns.Branches)
		for _, id := range sum.Patterns.Created {
			logf("pattern  %s", id)
		}
		for _, id := range sum.Patterns.Retired {
			logf("pattern retired %s (branch deleted, unmerged)", id)
		}
		for _, n := range sum.Patterns.Skipped {
			logf("pattern skipped %s", n)
		}
		sum.Notes = append(sum.Notes, sum.Patterns.Notes...)
	}
	if sum.Style.Ran {
		logf("style synthesis: %d group(s)", sum.Style.Groups)
		for _, id := range sum.Style.Created {
			logf("style candidate  %s", id)
		}
		for _, id := range sum.Style.Reinforced {
			logf("style reinforced %s", id)
		}
		for _, id := range sum.Style.Confirmed {
			logf("style confirmed  %s", id)
		}
		for _, id := range sum.Style.Retired {
			logf("style retired    %s (contradicted)", id)
		}
		for _, n := range sum.Style.Skipped {
			logf("style skipped    %s", n)
		}
		sum.Notes = append(sum.Notes, sum.Style.Notes...)
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
