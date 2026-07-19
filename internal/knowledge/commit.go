package knowledge

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Commit records the current state of the knowledge dir as one git commit
// with a structured message — the governance trail: `git log` answers when a
// card appeared, which pipeline wrote it, and why it changed. Automated
// commits are authored as "culi" so they are distinguishable from the user's
// own edits, and they work even without a global git identity.
//
// Best-effort by contract: callers must never fail a pipeline over history.
// A knowledge dir that is not a git repo returns an error the caller logs
// and moves on; a clean tree commits nothing and returns nil. The .import
// staging area is excluded — staged-but-unreviewed content is not knowledge
// yet (C4's review gate would be meaningless if staging were archived as if
// applied).
func Commit(kdir, message string) error {
	if _, err := os.Stat(filepath.Join(kdir, ".git")); err != nil {
		return fmt.Errorf("knowledge: %s is not a git repo (culi init creates it): %w", kdir, err)
	}
	ensureStagingIgnored(kdir)
	if err := gitRun(kdir, "add", "-A"); err != nil {
		return err
	}
	// Nothing staged ⇒ nothing to record.
	if gitRun(kdir, "diff", "--cached", "--quiet") == nil {
		return nil
	}
	return gitRun(kdir,
		"-c", "user.name=culi", "-c", "user.email=culi@localhost",
		"commit", "-q", "-m", message)
}

// ensureStagingIgnored guarantees the staging/drafts dirs are gitignored so
// `git add -A` (ours or the user's) can never archive unreviewed content.
// Idempotent; stores created before this shipped get the line on first
// commit.
func ensureStagingIgnored(kdir string) {
	path := filepath.Join(kdir, ".gitignore")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), ".import/") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(".import/\n.drafts/\n")
}

func gitRun(kdir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", kdir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		// Label with the subcommand, skipping -c config pairs.
		verb := args[0]
		for i := 0; i < len(args)-1; i += 2 {
			if args[i] != "-c" {
				verb = args[i]
				break
			}
		}
		return fmt.Errorf("knowledge: git %s: %w (%s)", verb, err, msg)
	}
	return nil
}
