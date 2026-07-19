package branchgen

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
	"github.com/hung12ct/culi/internal/learn/gitfacts"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

// fakeGen returns a canned payload per call name.
type fakeGen struct {
	payloads map[string]string // call name → JSON
	calls    map[string]int
}

func (g *fakeGen) ModelName() string { return "fake-model" }

func (g *fakeGen) Generate(_ context.Context, _, _, name string, _ map[string]any, out any) (llmgen.Usage, error) {
	if g.calls == nil {
		g.calls = map[string]int{}
	}
	g.calls[name]++
	p, ok := g.payloads[name]
	if !ok {
		return llmgen.Usage{}, errors.New("no payload for " + name)
	}
	return llmgen.Usage{Prompt: 100, Completion: 50}, json.Unmarshal([]byte(p), out)
}

const sectionsPayload = `{"sections":[{"id":"module-map","title":"Modules","markdown":"## Modules\n\n- internal/ingest: main pipeline"}],"notes":""}`
const cardsPayload = `{"cards":[{"slug":"ingest-changes","type":"pattern","title":"Ingest changes live in internal/ingest","summary":"New ingest behavior goes in internal/ingest with table tests.","markdown":"- Touch internal/ingest for pipeline changes\n- Add table tests alongside","keywords":["ingest","pipeline"],"confidence":0.9}],"notes":""}`

// scratchRepo builds a minimal git repo.
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
	if err := os.MkdirAll(filepath.Join(root, "internal", "ingest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "ingest", "i.go"), []byte("package ingest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "feat(ingest): init")
	return root
}

func newGenerator(t *testing.T, fake *fakeGen) *Generator {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(config.KnowledgeDir(base), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Generator{
		Base: base, Store: s, Logf: t.Logf,
		Tier: llmtier.NewTier(fake, fake, config.StateDir(base), 0, 100, false),
	}
}

func TestGenerateEndToEndAndNoOp(t *testing.T) {
	fake := &fakeGen{payloads: map[string]string{
		"claudemd_sections": sectionsPayload,
		"repo_cards":        cardsPayload,
	}}
	g := newGenerator(t, fake)
	root := scratchRepo(t)
	ctx := context.Background()

	facts, err := gitfacts.Collect(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Generate(ctx, facts, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.NoOp || len(res.Sections) != 1 || len(res.CardsWritten) != 1 {
		t.Fatalf("res = %+v", res)
	}

	// CLAUDE.md created with the span.
	raw, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(raw), "culi:begin id=module-map") {
		t.Fatalf("CLAUDE.md: %v\n%s", err, raw)
	}
	// Card on disk: gen provenance, repo scope, confirmed, indexed.
	card, err := knowledge.ReadCard(config.KnowledgeDir(g.Base), res.CardsWritten[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Provenance == nil || card.Provenance.Source != "gen" || card.Provenance.SourceHash != facts.Hash() {
		t.Errorf("provenance = %+v", card.Provenance)
	}
	if card.Scopes[0] != "repo:"+facts.Repo || card.Status != "confirmed" {
		t.Errorf("scope=%v status=%q", card.Scopes, card.Status)
	}
	if _, err := g.Store.CardByID(ctx, res.CardsWritten[0]); err != nil {
		t.Errorf("card not indexed: %v", err)
	}

	// Second run: facts unchanged → no-op, zero further model calls.
	res2, err := g.Generate(ctx, facts, "", false)
	if err != nil || !res2.NoOp {
		t.Fatalf("res2 = %+v, err %v", res2, err)
	}
	if fake.calls["claudemd_sections"] != 1 || fake.calls["repo_cards"] != 1 {
		t.Errorf("no-op still called the model: %v", fake.calls)
	}

	// Forced rerun with identical output: card skipped (same source hash),
	// CLAUDE.md byte-stable.
	before, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	res3, err := g.Generate(ctx, facts, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.CardsWritten) != 0 || len(res3.CardsSkipped) != 1 {
		t.Errorf("res3 = %+v", res3)
	}
	after, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(before) != string(after) {
		t.Error("forced identical regen changed CLAUDE.md bytes")
	}
}

func TestGenerateBranchModeCardsOnly(t *testing.T) {
	fake := &fakeGen{payloads: map[string]string{"repo_cards": cardsPayload}}
	g := newGenerator(t, fake)
	root := scratchRepo(t)
	ctx := context.Background()

	facts, err := gitfacts.Collect(ctx, root, "main")
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Generate(ctx, facts, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls["claudemd_sections"] != 0 {
		t.Error("branch mode drafted CLAUDE.md sections")
	}
	if len(res.CardsWritten) != 1 {
		t.Fatalf("res = %+v", res)
	}
	card, err := knowledge.ReadCard(config.KnowledgeDir(g.Base), res.CardsWritten[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(card.Scopes[0], "branch:") {
		t.Errorf("scope = %v", card.Scopes)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("branch mode wrote CLAUDE.md")
	}
}

func TestUpsertNeverClobbersForeignCard(t *testing.T) {
	fake := &fakeGen{payloads: map[string]string{
		"claudemd_sections": sectionsPayload,
		"repo_cards":        cardsPayload,
	}}
	g := newGenerator(t, fake)
	root := scratchRepo(t)
	ctx := context.Background()
	facts, err := gitfacts.Collect(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-place a hand-authored card at the deterministic gen path.
	rel := "patterns/gen-" + slugify(facts.Repo) + "-ingest-changes.md"
	hand := "---\ntitle: My own pattern\n---\n\nhand content\n"
	abs := filepath.Join(config.KnowledgeDir(g.Base), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := g.Generate(ctx, facts, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CardsWritten) != 0 || len(res.CardsSkipped) != 1 {
		t.Fatalf("res = %+v", res)
	}
	raw, _ := os.ReadFile(abs)
	if string(raw) != hand {
		t.Error("hand-authored card was clobbered (C4)")
	}
}
