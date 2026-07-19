package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

type fakeGen struct {
	payload string
	calls   int
	fail    bool
}

func (g *fakeGen) ModelName() string { return "fake-cheap" }

func (g *fakeGen) Generate(_ context.Context, _, user, _ string, _ map[string]any, out any) (llmgen.Usage, error) {
	g.calls++
	if g.fail {
		return llmgen.Usage{}, errors.New("boom")
	}
	if strings.Contains(user, "go.sum") {
		return llmgen.Usage{}, errors.New("noise leaked into model input")
	}
	return llmgen.Usage{Prompt: 100, Completion: 40}, json.Unmarshal([]byte(g.payload), out)
}

const patternPayload = `{"patterns":[{"slug":"add-probe","title":"Adding a collector probe","summary":"New probes go in internal/collect with a table test.","markdown":"- Add the probe in internal/collect\n- Register it in probes.go\n\n+func newProbe() {}","keywords":["probe","collector"],"confidence":0.85}],"notes":""}`

// repo builds a scratch repo: main + feature branch with clustered work and
// a noisy go.sum change.
func repo(t *testing.T) string {
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
	write("internal/collect/collect.go", "package collect\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat: base")

	run("checkout", "-q", "-b", "feat/probe")
	write("internal/collect/probe.go", "package collect\n\nfunc newProbe() {}\n")
	write("go.sum", "noise v1\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat(collect): add probe")
	write("internal/collect/probe_test.go", "package collect\n")
	run("add", "-A")
	run("commit", "-q", "-m", "test(collect): probe table test")
	run("checkout", "-q", "main")
	return root
}

func newRunner(t *testing.T, fake *fakeGen) *Runner {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config.KnowledgeDir(base), "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Runner{
		Base: base, Store: s, Logf: t.Logf,
		Tier: llmtier.NewTier(fake, fake, config.StateDir(base), 0, 100, false),
	}
}

func TestRunMinesMovedBranchThenNoOps(t *testing.T) {
	fake := &fakeGen{payload: patternPayload}
	r := newRunner(t, fake)
	root := repo(t)
	ctx := context.Background()

	res, err := r.Run(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branches != 1 || len(res.Created) != 1 || fake.calls != 1 {
		t.Fatalf("res = %+v calls=%d", res, fake.calls)
	}
	id := res.Created[0]
	if !strings.HasPrefix(id, "patterns/br-") || !strings.Contains(id, "feat-probe") {
		t.Errorf("id = %q", id)
	}
	card, err := knowledge.ReadCard(config.KnowledgeDir(r.Base), id+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "confirmed" || card.Provenance.Source != "patterns" ||
		card.Scopes[0] != "repo:"+filepath.Base(root) {
		t.Errorf("card = %+v", card)
	}
	if len(card.Provenance.MergedFrom) != 1 || !strings.HasPrefix(card.Provenance.MergedFrom[0], "feat/probe@") {
		t.Errorf("source = %v", card.Provenance.MergedFrom)
	}
	if _, err := r.Store.CardByID(ctx, id); err != nil {
		t.Errorf("not indexed: %v", err)
	}

	// Unmoved tips: zero further calls.
	res2, err := r.Run(ctx, []string{root})
	if err != nil || res2.Branches != 0 || fake.calls != 1 {
		t.Fatalf("second run = %+v calls=%d err=%v", res2, fake.calls, err)
	}
}

func TestRunRetiresDeletedUnmergedBranch(t *testing.T) {
	fake := &fakeGen{payload: patternPayload}
	r := newRunner(t, fake)
	root := repo(t)
	ctx := context.Background()

	res, err := r.Run(ctx, []string{root})
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("first run: %+v err=%v", res, err)
	}
	// Delete the branch WITHOUT merging.
	cmd := exec.Command("git", "-C", root, "branch", "-q", "-D", "feat/probe")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	res2, err := r.Run(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Retired) != 1 || res2.Retired[0] != res.Created[0] {
		t.Fatalf("retired = %v", res2.Retired)
	}
	card, _ := knowledge.ReadCard(config.KnowledgeDir(r.Base), res.Created[0]+".md")
	if card.Status != "retired" {
		t.Errorf("status = %q", card.Status)
	}
}

func TestRunKeepsCardsOfMergedDeletedBranch(t *testing.T) {
	fake := &fakeGen{payload: patternPayload}
	r := newRunner(t, fake)
	root := repo(t)
	ctx := context.Background()

	res, err := r.Run(ctx, []string{root})
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("first run: %+v err=%v", res, err)
	}
	for _, args := range [][]string{
		{"merge", "-q", "--no-edit", "feat/probe"},
		{"branch", "-q", "-d", "feat/probe"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	res2, err := r.Run(ctx, []string{root})
	if err != nil || len(res2.Retired) != 0 {
		t.Fatalf("merged deletion retired cards: %+v err=%v", res2, err)
	}
	card, _ := knowledge.ReadCard(config.KnowledgeDir(r.Base), res.Created[0]+".md")
	if card.Status != "confirmed" {
		t.Errorf("status = %q", card.Status)
	}
}

func TestRunFailedMineKeepsTipForRetry(t *testing.T) {
	fake := &fakeGen{fail: true}
	r := newRunner(t, fake)
	root := repo(t)
	ctx := context.Background()

	res, err := r.Run(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) == 0 || res.Branches != 0 {
		t.Fatalf("res = %+v", res)
	}
	// Tip not advanced: a later healthy run retries the branch.
	fake.fail = false
	fake.payload = patternPayload
	res2, err := r.Run(ctx, []string{root})
	if err != nil || res2.Branches != 1 || len(res2.Created) != 1 {
		t.Fatalf("retry run = %+v err=%v", res2, err)
	}
}

func TestRetirementDoesNotCollideAcrossBranchPrefixes(t *testing.T) {
	// Deleting unmerged branch "api" must not retire cards of live branch
	// "api-v2": the slugified filename prefix collides, the recorded
	// provenance branch does not.
	fake := &fakeGen{payload: patternPayload}
	r := newRunner(t, fake)
	root := repo(t)
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
	commitOn := func(branch, file string) {
		t.Helper()
		run("checkout", "-q", "-b", branch, "main")
		if err := os.WriteFile(filepath.Join(root, "internal", "collect", file), []byte("package collect\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-q", "-m", "feat(collect): "+file)
		run("checkout", "-q", "main")
	}
	commitOn("api", "api.go")
	commitOn("api-v2", "apiv2.go")

	res, err := r.Run(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branches != 3 { // feat/probe + api + api-v2
		t.Fatalf("res = %+v", res)
	}
	// Delete "api" unmerged; "api-v2" stays live.
	run("branch", "-q", "-D", "api")
	res2, err := r.Run(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range res2.Retired {
		if strings.Contains(id, "api-v2") {
			t.Fatalf("live branch card retired by prefix collision: %v", res2.Retired)
		}
	}
	kdir := config.KnowledgeDir(r.Base)
	card, err := knowledge.ReadCard(kdir, "patterns/"+branchCardPrefix(filepath.Base(root), "api-v2")+"add-probe.md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "confirmed" {
		t.Errorf("live api-v2 card status = %q", card.Status)
	}
	// The deleted branch's own card DID retire.
	dead, err := knowledge.ReadCard(kdir, "patterns/"+branchCardPrefix(filepath.Base(root), "api")+"add-probe.md")
	if err != nil {
		t.Fatal(err)
	}
	if dead.Status != "retired" {
		t.Errorf("deleted api card status = %q", dead.Status)
	}
}

func TestNoisyPathExcludesSecrets(t *testing.T) {
	for _, p := range []string{
		".env", ".env.local", "config/credentials.json", "certs/server.pem",
		"deploy/service.key", ".npmrc", "secrets.yaml",
	} {
		if !noisyPath(p) {
			t.Errorf("noisyPath(%q) = false, want true (secret-bearing)", p)
		}
	}
	if noisyPath("internal/env/env.go") {
		t.Error("env-named source dir wrongly excluded")
	}
}

func TestClusterUnitsSplitsByTimeAndDirs(t *testing.T) {
	commits := []commitInfo{
		{SHA: "a", Time: 1000, Dirs: map[string]bool{"internal/x": true}, Files: []string{"internal/x/a.go"}},
		{SHA: "b", Time: 2000, Dirs: map[string]bool{"internal/x": true}, Files: []string{"internal/x/b.go"}},
		// Different dirs → new unit even though adjacent in time.
		{SHA: "c", Time: 3000, Dirs: map[string]bool{"docs": true}, Files: []string{"docs/d.md"}},
		// Huge time gap → new unit despite same dirs.
		{SHA: "d", Time: 3000 + unitTimeGap + 1, Dirs: map[string]bool{"docs": true}, Files: []string{"docs/e.md"}},
	}
	units := clusterUnits(commits)
	if len(units) != 3 {
		t.Fatalf("units = %d: %+v", len(units), units)
	}
	if len(units[0].Commits) != 2 || units[0].Dirs[0] != "internal/x" {
		t.Errorf("largest unit = %+v", units[0])
	}
}

func TestNoisyPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"go.sum", true},
		{"sub/package-lock.json", true},
		{"vendor/x/y.go", true},
		{"api/gen.pb.go", true},
		{"internal/x/handler.go", false},
		{"a => b/renamed.go", true},
	} {
		if got := noisyPath(tc.path); got != tc.want {
			t.Errorf("noisyPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWriteCardNeverClobbersForeign(t *testing.T) {
	fake := &fakeGen{payload: patternPayload}
	r := newRunner(t, fake)
	root := repo(t)

	rel := "patterns/" + branchCardPrefix(filepath.Base(root), "feat/probe") + "add-probe.md"
	hand := "---\ntitle: My pattern\n---\n\nhand content\n"
	abs := filepath.Join(config.KnowledgeDir(r.Base), filepath.FromSlash(rel))
	if err := os.WriteFile(abs, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v", res)
	}
	raw, _ := os.ReadFile(abs)
	if string(raw) != hand {
		t.Error("hand-authored card clobbered (C4)")
	}
}
