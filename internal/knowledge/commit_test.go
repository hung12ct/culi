package knowledge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestCommit(t *testing.T) {
	kdir := t.TempDir()
	gitOut(t, kdir, "init", "-q")
	if err := os.WriteFile(filepath.Join(kdir, ".gitignore"), []byte(".import/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kdir, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, "rules", "x.md"), []byte("---\ntitle: X\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Staged import content must never be archived as if applied.
	if err := os.MkdirAll(filepath.Join(kdir, ".import", "staged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, ".import", "staged", "s.md"), []byte("staged"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Commit(kdir, "learn: 1 mined candidate"); err != nil {
		t.Fatal(err)
	}
	log := gitOut(t, kdir, "log", "--format=%an|%s")
	if !strings.Contains(log, "culi|learn: 1 mined candidate") {
		t.Errorf("log = %q", log)
	}
	files := gitOut(t, kdir, "ls-files")
	if strings.Contains(files, ".import") {
		t.Errorf("staging area committed: %q", files)
	}
	if !strings.Contains(files, "rules/x.md") {
		t.Errorf("card not committed: %q", files)
	}

	// Clean tree: no new commit, no error.
	if err := Commit(kdir, "noop"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(gitOut(t, kdir, "log", "--format=%s"), "\n"); n != 1 {
		t.Errorf("clean-tree commit created a commit: %d entries", n)
	}

	// Mutation commits again with the new message.
	if err := os.WriteFile(filepath.Join(kdir, "rules", "x.md"), []byte("---\ntitle: X\nstatus: retired\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Commit(kdir, "review: rejected rules/x"); err != nil {
		t.Fatal(err)
	}
	if log := gitOut(t, kdir, "log", "--format=%s", "-n", "1"); !strings.Contains(log, "review: rejected") {
		t.Errorf("head = %q", log)
	}
}

func TestCommitOutsideRepoErrors(t *testing.T) {
	if err := Commit(t.TempDir(), "msg"); err == nil {
		t.Fatal("want error for non-repo knowledge dir")
	}
}
