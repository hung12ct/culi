package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

// startSession spins a full client↔server pair over in-memory transports
// against a sandbox CULI_HOME seeded with one rule and one skill card.
func startSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	kdir := config.KnowledgeDir(base)

	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(kdir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/go-err.md", "---\ntitle: Wrap Go errors\nscope: [global]\nsummary: wrap errors with package prefix\n---\nAlways wrap with fmt.Errorf and %w.\n")
	write("skills/deploy/SKILL.md", "---\ntitle: Deploy skill\nscope: [global]\nsummary: deployment runbook steps\n---\nSee checklist.md for the full runbook.\n")
	write("skills/deploy/checklist.md", "1. build\n2. ship\n")

	srv, err := New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if _, err := indexer.Sync(ctx, srv.store, kdir); err != nil {
		t.Fatal(err)
	}

	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, base
}

func call(t *testing.T, sess *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %+v", tool, res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: decoding structured content: %v", tool, err)
	}
}

func TestSearchExpandSaveFlow(t *testing.T) {
	sess, base := startSession(t)
	ctx := context.Background()

	// search_context finds the rule (BM25; Ollama absent in tests degrades).
	var search searchOut
	call(t, sess, "search_context", map[string]any{"query": "how to wrap errors with package prefix"}, &search)
	if len(search.Results) == 0 || search.Results[0].ID != "rules/go-err" {
		t.Fatalf("search results: %+v", search)
	}

	// expand_card by short ID returns the body and bumps utility.
	var exp expandOut
	call(t, sess, "expand_card", map[string]any{"id": search.Results[0].ShortID}, &exp)
	if !strings.Contains(exp.Body, "fmt.Errorf") {
		t.Fatalf("expand body: %+v", exp)
	}
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	utils, err := s.UtilityMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if utils["rules/go-err"] <= 1.0 {
		t.Errorf("expand_card should raise utility, got %v", utils["rules/go-err"])
	}

	// Skill expansion lists attachments as absolute paths.
	var skill expandOut
	call(t, sess, "expand_card", map[string]any{"id": "skills/deploy"}, &skill)
	if len(skill.Attachments) != 1 || !strings.HasSuffix(skill.Attachments[0], "checklist.md") {
		t.Fatalf("skill attachments: %+v", skill.Attachments)
	}

	// save_lesson writes a confirmed card and reindexes it immediately.
	var saved saveOut
	call(t, sess, "save_lesson", map[string]any{
		"title":    "Prefer table tests",
		"markdown": "Use t.Run subtests for every case matrix.",
		"scope":    "lang:go",
	}, &saved)
	if _, err := os.Stat(saved.Path); err != nil {
		t.Fatalf("lesson file missing: %v", err)
	}
	if c, err := s.CardByID(ctx, saved.ID); err != nil || c.Status != "confirmed" || c.Type != "lesson" {
		t.Fatalf("lesson not indexed as confirmed lesson: %+v (%v)", c, err)
	}

	// Saving the same title again must not clobber the first file (C4).
	var saved2 saveOut
	call(t, sess, "save_lesson", map[string]any{
		"title":    "Prefer table tests",
		"markdown": "Different content this time.",
	}, &saved2)
	if saved2.Path == saved.Path {
		t.Fatal("second save overwrote the first lesson")
	}

	// Invalid scope is a tool error, not a crash.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "save_lesson", Arguments: map[string]any{
		"title": "x", "markdown": "y", "scope": "branch:weird"}})
	if err != nil || !res.IsError {
		t.Fatalf("invalid scope should be a tool error (err=%v res=%+v)", err, res)
	}
}

// TestSaveLessonMergesNearDuplicate proves the smart-dedup path: a second
// save_lesson about the same thing (same scope, near-identical title+summary)
// folds into the existing card instead of creating a duplicate. No Ollama in
// tests, so this exercises the lexical-Jaccard fallback.
func TestSaveLessonMergesNearDuplicate(t *testing.T) {
	sess, base := startSession(t)
	kdir := config.KnowledgeDir(base)

	var first saveOut
	call(t, sess, "save_lesson", map[string]any{
		"title":    "Connect to tagby AWS",
		"summary":  "bastion aws ssm start-session profile tagby-prod",
		"markdown": "Use the bastion host, then aws ssm start-session with profile tagby-prod.",
		"scope":    "repo:tagby-backend",
	}, &first)
	if first.Merged {
		t.Fatalf("first save should create, not merge: %+v", first)
	}

	var second saveOut
	call(t, sess, "save_lesson", map[string]any{
		"title":    "Connect to tagby AWS",
		"summary":  "bastion aws ssm start-session profile tagby-prod vpn",
		"markdown": "You also need the corp VPN up before the bastion is reachable.",
		"scope":    "repo:tagby-backend",
	}, &second)
	if !second.Merged {
		t.Fatalf("near-duplicate save should merge, not duplicate: %+v", second)
	}
	if second.ID != first.ID {
		t.Fatalf("merge target = %s, want existing %s", second.ID, first.ID)
	}

	// Exactly one lesson file, holding both the original and the appended knowledge.
	files, _ := filepath.Glob(filepath.Join(kdir, "lessons", "*", "*.md"))
	if len(files) != 1 {
		t.Fatalf("lesson files = %d, want 1 (no duplicate): %v", len(files), files)
	}
	body, _ := os.ReadFile(files[0])
	if !strings.Contains(string(body), "VPN") || !strings.Contains(string(body), "**Update ") {
		t.Fatalf("appended knowledge / update marker missing:\n%s", body)
	}

	// A different-scope save with the same title must NOT merge (scopes are isolated).
	var other saveOut
	call(t, sess, "save_lesson", map[string]any{
		"title":    "Connect to tagby AWS",
		"summary":  "bastion aws ssm start-session profile tagby-prod",
		"markdown": "Global note.",
		"scope":    "global",
	}, &other)
	if other.Merged {
		t.Fatalf("cross-scope save must not merge: %+v", other)
	}
}
