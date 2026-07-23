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
	"github.com/hung12ct/culi/internal/learn/codexscan"
	"github.com/hung12ct/culi/internal/learn/queue"
)

// learnTimeout bounds one worker run: a wedged backend must not leave a
// zombie holding the lock forever (the lock's staleness guard is the backstop).
const learnTimeout = 15 * time.Minute

// noCapTimeout gives a --no-cap run room to drain a large backlog in one pass
// (mining is one model call per transcript window).
const noCapTimeout = 2 * time.Hour

// Learn drains the learning inbox once. --auto is the detached mode the
// session-end hook spawns: all output goes to logs/learn.log, exit is always
// clean (a background learner must never surface errors into a terminal that
// isn't there).
func Learn(args []string) error {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fromStart := fs.Bool("from-start", false, "ignore cursors and re-mine transcripts from the beginning")
	forceStyle := fs.Bool("style", false, "force style synthesis now (bypass the weekly/15-observation trigger)")
	auto := fs.Bool("auto", false, "background mode: log to ~/.culi/logs/learn.log (used by the session-end hook)")
	noCap := fs.Bool("no-cap", false, "ignore the daily USD/call caps — mine the whole backlog in one run")
	scanCodex := fs.Bool("scan-codex", false, "discover and enqueue Codex rollout history before learning")
	dryRun := fs.Bool("dry-run", false, "with --scan-codex, list discovered sessions without writing or learning")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	if *dryRun && !*scanCodex {
		return fmt.Errorf("cli: --dry-run requires --scan-codex")
	}

	var codexSessions []codexscan.Session
	var codexSkipped int
	if *scanCodex {
		scanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		codexSessions, codexSkipped, err = codexscan.Discover(scanCtx, codexHome())
		cancel()
		if err != nil {
			return err
		}
		if *dryRun {
			fmt.Printf("Codex sessions: %d\n", len(codexSessions))
			for _, session := range codexSessions {
				fmt.Printf("  codex:%s  %s  %s\n", session.SessionID,
					session.UpdatedAt.Format(time.RFC3339), session.RolloutPath)
			}
			if codexSkipped > 0 {
				fmt.Printf("  skipped %d rollout(s) outside %s or unreadable\n", codexSkipped, codexHome())
			}
			return nil
		}
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}

	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	if *auto {
		logf = fileLogf(base, "learn.log")
	}
	if len(codexSessions) > 0 {
		for _, session := range codexSessions {
			job := queue.Job{
				SessionID: "codex:" + session.SessionID, TranscriptPath: session.RolloutPath,
				CWD: session.CWD, Source: "codex", Trigger: "session-end",
				EnqueuedAt: session.UpdatedAt.Format(time.RFC3339),
			}
			if err := queue.Enqueue(config.InboxDir(base), job); err != nil {
				return err
			}
		}
		logf("codex scan: %d transcript(s) queued", len(codexSessions))
	}
	if codexSkipped > 0 {
		logf("codex scan: skipped %d rollout(s) outside %s or unreadable", codexSkipped, codexHome())
	}

	timeout := learnTimeout
	if *noCap {
		timeout = noCapTimeout // room to drain a large backlog in one run
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sum, err := learn.Run(ctx, base, cfg, learn.Options{FromStart: *fromStart, ForceStyle: *forceStyle, IgnoreCaps: *noCap}, logf)
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
