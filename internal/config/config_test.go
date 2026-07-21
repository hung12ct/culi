package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestCandidateTTLDefaulting(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want int
	}{
		{"omitted defaults to 30", "learn:\n  enabled: true\n", defaultCandidateTTL},
		{"explicit zero defaults to 30", "learn:\n  candidate_ttl_days: 0\n", defaultCandidateTTL},
		{"negative disables (survives)", "learn:\n  candidate_ttl_days: -1\n", -1},
		{"explicit positive kept", "learn:\n  candidate_ttl_days: 7\n", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeCfg(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Learn.CandidateTTLDays != tc.want {
				t.Errorf("CandidateTTLDays = %d, want %d", cfg.Learn.CandidateTTLDays, tc.want)
			}
		})
	}
}

func TestConfirmAtDefaulting(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want int
	}{
		{"omitted defaults to 2", "learn:\n  enabled: true\n", defaultConfirmAt},
		{"explicit zero defaults to 2", "learn:\n  confirm_at: 0\n", defaultConfirmAt},
		{"negative defaults to 2 (no disable semantics)", "learn:\n  confirm_at: -3\n", defaultConfirmAt},
		{"one kept (confirm on first sighting)", "learn:\n  confirm_at: 1\n", 1},
		{"higher kept", "learn:\n  confirm_at: 4\n", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeCfg(t, tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Learn.ConfirmAt != tc.want {
				t.Errorf("ConfirmAt = %d, want %d", cfg.Learn.ConfirmAt, tc.want)
			}
		})
	}
}

func TestMissingConfigDefaultsTTL(t *testing.T) {
	// No config.yaml at all: defaults apply, janitor enabled at 30.
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Learn.CandidateTTLDays != defaultCandidateTTL {
		t.Errorf("CandidateTTLDays = %d, want %d", cfg.Learn.CandidateTTLDays, defaultCandidateTTL)
	}
}
