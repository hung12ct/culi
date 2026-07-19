package gitfacts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scratchRepo builds a small git repo with conventional commits across two
// modules, a go.mod, a Makefile, and a CLAUDE.md containing one culi span.
func scratchRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	write("go.mod", "module example.com/x\n\ngo 1.24\n\nrequire (\n\tgopkg.in/yaml.v3 v3.0.1\n\tgolang.org/x/sync v0.21.0 // indirect\n)\n")
	write("Makefile", "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n")
	write("CLAUDE.md", "# X\n\nHand-written intro.\n\n<!-- culi:begin id=gen hash=abcd1234 -->\ngenerated stuff\n<!-- culi:end id=gen -->\n")
	write("internal/ingest/ingest.go", "package ingest\n")
	write("internal/ingest/ingest_test.go", "package ingest\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat(ingest): initial ingest module")

	write("internal/api/api.go", "package api\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat(api): add api surface")

	write("internal/ingest/more.go", "package ingest\n")
	run("add", "-A")
	run("commit", "-q", "-m", "fix(ingest): handle empty input")

	// Feature branch with one unique commit.
	run("checkout", "-q", "-b", "feat/parser")
	write("internal/parser/parser.go", "package parser\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat(parser): skeleton")
	run("checkout", "-q", "main")
	return root
}

func TestCollectAndRender(t *testing.T) {
	root := scratchRepo(t)
	f, err := Collect(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo != filepath.Base(root) || f.Branch != "main" {
		t.Errorf("repo=%q branch=%q", f.Repo, f.Branch)
	}

	r := f.Render()
	for _, want := range []string{
		"internal/ingest — 2 commits", // module churn with per-commit dedup
		"internal/api",
		"Conventional commits (100%",
		"feat (", "fix (",
		"gopkg.in/yaml.v3", // direct dep
		"Makefile targets: build, test.",
		"Go test files: 1",
		"Hand-written intro.",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("render missing %q:\n%s", want, r)
		}
	}
	// Generated span content and indirect deps stay out.
	for _, ban := range []string{"generated stuff", "golang.org/x/sync"} {
		if strings.Contains(r, ban) {
			t.Errorf("render leaked %q", ban)
		}
	}
	// Hash is stable across identical collections.
	f2, _ := Collect(context.Background(), root, "")
	if f.Hash() != f2.Hash() {
		t.Error("hash unstable for unchanged repo")
	}
}

func TestCollectBranchWork(t *testing.T) {
	root := scratchRepo(t)
	f, err := Collect(context.Background(), root, "feat/parser")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.BranchWork, "feat(parser): skeleton") ||
		!strings.Contains(f.BranchWork, "internal/parser") {
		t.Errorf("branch work = %q", f.BranchWork)
	}
	// Branch facts hash differs from repo-level facts.
	base, _ := Collect(context.Background(), root, "")
	if f.Hash() == base.Hash() {
		t.Error("branch facts should hash differently")
	}
}

func TestCollectNotARepo(t *testing.T) {
	if _, err := Collect(context.Background(), t.TempDir(), ""); err == nil {
		t.Fatal("want error for non-repo")
	}
}

func TestModuleDir(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"main.go", "."},
		{"internal/retrieve/fuse.go", "internal/retrieve"},
		{"cmd/culi/main.go", "cmd/culi"},
		{"docs/readme.md", "docs"},
		{"internal/x", "internal"}, // file directly under an umbrella dir
	} {
		if got := moduleDir(tc.in); got != tc.want {
			t.Errorf("moduleDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
