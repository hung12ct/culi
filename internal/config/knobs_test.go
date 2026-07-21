package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKnobsPreservesCommentsAndSetsNestedKeys(t *testing.T) {
	dir := t.TempDir()
	original := `# culi config
push_budget: 700   # tokens per prompt
learn:
  # background mining
  provider: auto
  daily_call_cap: 40
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := SetKnobs(dir, map[string]string{
		"push_budget":      "900",                            // top-level int, existing
		"oauth_token_file": "~/.claude-tokens/account.token", // nested string, NEW under learn
		"daily_call_cap":   "25",                             // nested int, existing
		"bogus_key":        "ignored",                        // not whitelisted → skipped
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("applied = %d, want 3 (bogus_key ignored)", n)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"# culi config",       // header comment preserved
		"# tokens per prompt", // inline comment preserved
		"# background mining", // nested comment preserved
		"push_budget: 900",    // updated
		"daily_call_cap: 25",  // updated nested
		"oauth_token_file: ~/.claude-tokens/account.token", // new nested key added
		"provider: auto", // untouched sibling preserved
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "bogus_key") {
		t.Errorf("non-whitelisted key was written:\n%s", s)
	}

	// The reloaded config reflects the writes.
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushBudget != 900 || cfg.Learn.DailyCallCap != 25 ||
		cfg.Learn.OAuthTokenFile != "~/.claude-tokens/account.token" {
		t.Errorf("reloaded = %+v", cfg)
	}
}

func TestSetKnobsRejectsBadValueAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "push_budget: 700\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetKnobs(dir, map[string]string{"daily_usd_cap": "not-a-number"}); err == nil {
		t.Fatal("want parse error")
	}
	// File must be untouched on a parse failure.
	out, _ := os.ReadFile(path)
	if string(out) != original {
		t.Errorf("config.yaml was modified on error:\n%s", out)
	}
}
