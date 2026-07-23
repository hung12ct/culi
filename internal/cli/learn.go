package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/harness"
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

// Codex Stop fires frequently. Automatic rollout recovery shares that cadence
// but performs read-only discovery at most once per interval; direct hook jobs
// still drain on every worker run.
const autoCodexScanInterval = 10 * time.Minute

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
	scanCodexForce := fs.Bool("scan-codex-force", false, "force an automatic final Codex scan (internal lifecycle use)")
	dryRun := fs.Bool("dry-run", false, "with --scan-codex, list discovered sessions without writing or learning")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	if *dryRun && !*scanCodex {
		return fmt.Errorf("cli: --dry-run requires --scan-codex")
	}
	if *scanCodexForce && (!*auto || !*scanCodex) {
		return fmt.Errorf("cli: --scan-codex-force requires --auto --scan-codex")
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	if *auto {
		logf = fileLogf(base, "learn.log")
	}

	if *scanCodex {
		if stop, scanErr := runCodexScan(base, *auto, *dryRun, *scanCodexForce, logf); stop {
			return scanErr
		}
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
			"terminal: run `codex login` or `claude auth login`; API: set OPENAI_API_KEY or ANTHROPIC_API_KEY.")
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

// runCodexScan performs the throttled, read-only Codex rollout recovery: acquire
// the scan lease, discover sessions, enqueue the ones the queue has not seen, and
// record content-free health. It returns stop=true when Learn should return
// immediately — a --dry-run listing (err nil) or a non-auto failure (err set). In
// --auto a discovery failure is logged and swallowed (stop=false) so the worker
// still drains already-queued jobs. The lease is released (deferred) before the
// caller's long learn.Run, so a 15min–2h drain never holds the scan lock. Error
// log lines are scrubbed of $CULI_HOME/$CODEX_HOME paths for parity with the
// durable health record.
func runCodexScan(base string, auto, dryRun, force bool, logf func(string, ...any)) (bool, error) {
	if !dryRun {
		interval := time.Duration(0)
		if auto && !force {
			interval = autoCodexScanInterval
		}
		lease, due, err := acquireCodexScan(config.StateDir(base), interval, force)
		if errors.Is(err, codexscan.ErrScanLocked) && auto {
			if force {
				logf("codex scan: final recovery skipped because another scan is still running")
			}
			return false, nil
		}
		if err != nil {
			return true, err
		}
		if lease != nil {
			defer func() { _ = lease.Release() }()
		}
		if !due {
			return false, nil
		}
	}

	scanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	started := time.Now().UTC()
	sessions, skipped, err := codexscan.Discover(scanCtx, codexHome())
	cancel()
	if err != nil {
		if dryRun {
			return true, err
		}
		if healthErr := recordCodexScan(base, started, auto, 0, 0, 0, err); healthErr != nil && auto {
			logf("codex scan health: %v", healthErr)
		}
		if !auto {
			return true, err
		}
		logf("codex scan: %v", safeScanError(err, base, codexHome()))
		return false, nil
	}
	if dryRun {
		fmt.Printf("Codex sessions: %d\n", len(sessions))
		for _, session := range sessions {
			fmt.Printf("  %s  %s  %s\n", harness.Codex.PrefixSession(session.SessionID),
				session.UpdatedAt.Format(time.RFC3339), session.RolloutPath)
		}
		if skipped > 0 {
			fmt.Printf("  skipped %d rollout(s) outside %s or unreadable\n", skipped, codexHome())
		}
		return true, nil
	}

	queued, queueErr := enqueueCodexSessions(base, sessions)
	recErr := recordCodexScan(base, started, auto, len(sessions), queued, skipped, queueErr)
	if queueErr != nil {
		if !auto {
			return true, queueErr
		}
		logf("codex scan: %v", safeScanError(queueErr, base, codexHome()))
	}
	if recErr != nil {
		if !auto {
			return true, recErr
		}
		logf("codex scan health: %v", recErr)
	}
	logf("codex scan: %d discovered, %d transcript(s) queued", len(sessions), queued)
	if skipped > 0 {
		logf("codex scan: skipped %d rollout(s) outside %s or unreadable", skipped, codexHome())
	}
	return false, nil
}

func acquireCodexScan(stateDir string, interval time.Duration, wait bool) (*codexscan.Lease, bool, error) {
	deadline := time.Now().Add(6 * time.Second)
	for {
		lease, due, err := codexscan.Acquire(stateDir, time.Now().UTC(), interval)
		if !errors.Is(err, codexscan.ErrScanLocked) || !wait || time.Now().After(deadline) {
			return lease, due, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func enqueueCodexSessions(base string, sessions []codexscan.Session) (int, error) {
	stateDir := config.StateDir(base)
	inboxDir := config.InboxDir(base)
	cursors := queue.LoadCursors(stateDir)
	pending := queue.ScannerBlockedTranscripts(inboxDir)
	queued := 0
	for _, session := range sessions {
		info, err := os.Stat(session.RolloutPath)
		if err != nil {
			continue // Discover already validated it; a concurrent removal is benign.
		}
		_, alreadyPending := pending[session.RolloutPath]
		if cursors.Get(session.RolloutPath).Offset >= info.Size() || alreadyPending {
			continue
		}
		job := queue.Job{
			SessionID: harness.Codex.PrefixSession(session.SessionID), TranscriptPath: session.RolloutPath,
			CWD: session.CWD, Source: harness.Codex, Trigger: "session-end",
			EnqueuedAt: session.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if err := queue.Enqueue(inboxDir, job); err != nil {
			return queued, err
		}
		pending[session.RolloutPath] = struct{}{}
		queued++
	}
	return queued, nil
}

func recordCodexScan(base string, started time.Time, auto bool, discovered, queued, skipped int, scanErr error) error {
	stateDir := config.StateDir(base)
	h, _ := codexscan.LoadHealth(stateDir)
	h.LastAttempt = started
	h.Mode = "manual"
	if auto {
		h.Mode = "auto"
	}
	h.Discovered = discovered
	h.Queued = queued
	h.Skipped = skipped
	h.DurationMS = time.Since(started).Milliseconds()
	if scanErr != nil {
		h.Error = safeScanError(scanErr, base, codexHome())
	} else {
		h.Error = ""
		h.LastSuccess = time.Now().UTC()
	}
	return codexscan.SaveHealth(stateDir, h)
}

func safeScanError(err error, base, codexDir string) string {
	s := err.Error()
	if base != "" {
		s = strings.ReplaceAll(s, base, "$CULI_HOME")
	}
	if codexDir != "" {
		s = strings.ReplaceAll(s, codexDir, "$CODEX_HOME")
	}
	return s
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
