package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFullFrontmatter(t *testing.T) {
	raw := []byte(`---
id: rule/go-error-wrapping
type: rule
title: Wrap errors with package prefix
scope: [lang:go]
key: error-wrapping
triggers:
  keywords: [error handling, fmt.Errorf]
  globs: ["**/*.go"]
aliases: [xử lý lỗi]
summary: >-
  Errors wrapped as fmt.Errorf("pkg: ...: %w").
baseline: true
---
Full body here.
`)
	c, err := Parse("rules/go-error-wrapping.md", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ID != "rule/go-error-wrapping" {
		t.Errorf("ID = %q", c.ID)
	}
	if c.Type != "rule" || c.Key != "error-wrapping" || !c.Baseline {
		t.Errorf("meta = %+v", c)
	}
	if len(c.Scopes) != 1 || c.Scopes[0] != "lang:go" {
		t.Errorf("Scopes = %v", c.Scopes)
	}
	if len(c.Triggers.Keywords) != 2 || len(c.Triggers.Globs) != 1 {
		t.Errorf("Triggers = %+v", c.Triggers)
	}
	if c.Aliases[0] != "xử lý lỗi" {
		t.Errorf("Aliases = %v", c.Aliases)
	}
	if c.Body != "Full body here." {
		t.Errorf("Body = %q", c.Body)
	}
	if c.TokSummary == 0 || c.TokBody == 0 || c.ContentHash == "" {
		t.Errorf("derived fields missing: %+v", c)
	}
}

func TestParseDefaults(t *testing.T) {
	tests := []struct {
		path     string
		raw      string
		wantID   string
		wantType string
	}{
		{"lessons/2026/gorm-save.md", "---\ntitle: T\n---\nbody", "lessons/2026/gorm-save", "lesson"},
		{"skills/tdd/SKILL.md", "---\ntitle: T\n---\nbody", "skills/tdd", "skill"},
		{"styles/naming.md", "---\ntitle: T\n---\nbody", "styles/naming", "style"},
		{"loose.md", "---\ntitle: T\n---\nbody", "loose", "rule"},
	}
	for _, tt := range tests {
		c, err := Parse(tt.path, []byte(tt.raw))
		if err != nil {
			t.Fatalf("%s: %v", tt.path, err)
		}
		if c.ID != tt.wantID || c.Type != tt.wantType {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", tt.path, c.ID, c.Type, tt.wantID, tt.wantType)
		}
		if len(c.Scopes) != 1 || c.Scopes[0] != "global" {
			t.Errorf("%s: default scope = %v", tt.path, c.Scopes)
		}
		if c.Summary != "body" { // degraded from first body line
			t.Errorf("%s: Summary = %q", tt.path, c.Summary)
		}
	}
}

func TestParsePermissive(t *testing.T) {
	// Unknown keys tolerated; no frontmatter at all is body-only but fails on
	// missing title; unterminated frontmatter errors (caller skips the card).
	if _, err := Parse("rules/x.md", []byte("---\ntitle: X\nunknown_key: [a, b]\nnested:\n  deep: true\n---\nbody")); err != nil {
		t.Errorf("unknown keys should be tolerated: %v", err)
	}
	if _, err := Parse("rules/x.md", []byte("just a body, no title")); err == nil {
		t.Error("missing title should error")
	}
	if _, err := Parse("rules/x.md", []byte("---\ntitle: X\nno terminator")); err == nil {
		t.Error("unterminated frontmatter should error")
	}
}

func TestEstimateTokensVietnamese(t *testing.T) {
	en := "wrap errors with a package prefix on every boundary"
	vi := "luôn bọc lỗi với tiền tố của gói ở mọi ranh giới"
	if got := EstimateTokens(en); got < 8 || got > 20 {
		t.Errorf("en estimate out of range: %d", got)
	}
	// Vietnamese is token-denser: runes/1.8, not bytes/4 (bytes ≈ 1.4x runes).
	viTok := EstimateTokens(vi)
	if viTok < utf8Runes(vi)/3 || viTok > utf8Runes(vi) {
		t.Errorf("vi estimate out of range: %d for %d runes", viTok, utf8Runes(vi))
	}
}

func utf8Runes(s string) int { return len([]rune(s)) }

func TestNarrowestScopeRank(t *testing.T) {
	allowed := map[string]bool{"global": true, "lang:go": true, "repo:culi": true, "branch:culi@main": true}
	tests := []struct {
		scopes []string
		want   int
	}{
		{[]string{"global"}, 0},
		{[]string{"lang:go"}, 1},
		{[]string{"repo:culi"}, 2},
		{[]string{"branch:culi@main"}, 3},
		{[]string{"global", "repo:culi"}, 2},
		{[]string{"repo:other"}, -1}, // out of scope entirely
	}
	for _, tt := range tests {
		c := Card{Scopes: tt.scopes}
		if got := c.NarrowestScopeRank(allowed); got != tt.want {
			t.Errorf("%v: rank = %d, want %d", tt.scopes, got, tt.want)
		}
	}
}

func TestWalkSkipsHiddenAndNonCards(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/a.md", "---\ntitle: A\n---\nx")
	write("skills/tdd/SKILL.md", "---\ntitle: T\n---\nx")
	write("skills/tdd/template.md", "attachment, not a card")
	write(".import/staged/b.md", "staged, not indexed")
	write("notes.txt", "not markdown")

	files, err := Walk(dir)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.RelPath] = true
	}
	if len(got) != 2 || !got["rules/a.md"] || !got["skills/tdd/SKILL.md"] {
		t.Errorf("Walk = %v", got)
	}
}

func TestWalkMissingDir(t *testing.T) {
	files, err := Walk("/nonexistent/culi-test-dir")
	if err != nil || files != nil {
		t.Errorf("missing dir should be empty, got %v, %v", files, err)
	}
}
