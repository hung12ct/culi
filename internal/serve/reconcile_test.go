package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/config"
)

func TestStagingViewClassifies(t *testing.T) {
	kdir := t.TempDir()
	staged := filepath.Join(kdir, ".import", "staged")
	mk := func(rel, content string) {
		p := filepath.Join(staged, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkLive := func(rel, content string) {
		p := filepath.Join(kdir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A brand-new card (no live counterpart) → ready.
	mk("rules/new.md", "fresh\n")
	// A card identical to its live version → ready (apply would drain it).
	mk("rules/same.md", "identical\n")
	mkLive("rules/same.md", "identical\n")
	// A card that differs from live → conflict, with line deltas.
	mk("rules/diff.md", "line1\nCHANGED\nline3\n")
	mkLive("rules/diff.md", "line1\nline2\nline3\n")
	// A residual CLAUDE.md → residual, matched to a watched repo.
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("live claude md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk("residual/"+filepath.Base(repo)+".CLAUDE.md", "new claude md\n")

	s := &server{kdir: kdir, cfg: config.Config{Repos: []string{repo}}}
	st := s.stagingView()

	if !st.Present {
		t.Fatal("staging should be present")
	}
	if len(st.Ready) != 2 {
		t.Errorf("ready = %d, want 2 (new + identical): %+v", len(st.Ready), st.Ready)
	}
	if len(st.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(st.Conflicts))
	}
	c := st.Conflicts[0]
	if c.Rel != "rules/diff.md" || c.Added != 1 || c.Removed != 1 {
		t.Errorf("conflict = %+v, want rel=rules/diff.md +1/-1", c)
	}
	if c.Live == "" || c.Staged == "" {
		t.Error("conflict must carry both live and staged bodies for the diff view")
	}
	if len(st.Residuals) != 1 {
		t.Fatalf("residuals = %d, want 1", len(st.Residuals))
	}
	r := st.Residuals[0]
	if r.Missing || r.Repo != filepath.Base(repo) || r.Live != "live claude md\n" {
		t.Errorf("residual = %+v, want matched to repo with live body", r)
	}
}

func TestStagingViewAbsent(t *testing.T) {
	s := &server{kdir: t.TempDir(), cfg: config.Config{}}
	if st := s.stagingView(); st.Present {
		t.Error("no staging dir → Present should be false")
	}
}

func TestLineDelta(t *testing.T) {
	add, rem := lineDelta("a\nb\nc\n", "a\nX\nc\nd\n")
	// staged adds X and d (2), removes b (1).
	if add != 2 || rem != 1 {
		t.Errorf("lineDelta = +%d/-%d, want +2/-1", add, rem)
	}
	if a, r := lineDelta("same\n", "same\n"); a != 0 || r != 0 {
		t.Errorf("identical delta = +%d/-%d, want 0/0", a, r)
	}
}

func TestClampMergeDone(t *testing.T) {
	if got := clampMergeDone(25, 20); got != 20 {
		t.Errorf("clamp(25,20) = %d, want 20", got)
	}
	if got := clampMergeDone(5, 20); got != 5 {
		t.Errorf("clamp(5,20) = %d, want 5", got)
	}
	if got := clampMergeDone(5, 0); got != 5 {
		t.Errorf("clamp(5,0) = %d, want 5 (unknown total)", got)
	}
}
