package llmtier

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/llmgen"
)

// fakeGen counts calls and returns fixed usage.
type fakeGen struct {
	name    string
	calls   int
	fail    bool
	failMsg string // custom error text when fail is set (default "boom")
}

func (g *fakeGen) ModelName() string { return g.name }

func (g *fakeGen) Generate(_ context.Context, _, _, _ string, _ map[string]any, _ any) (llmgen.Usage, error) {
	g.calls++
	u := llmgen.Usage{Prompt: 1000, Completion: 500}
	if g.fail {
		msg := g.failMsg
		if msg == "" {
			msg = "boom"
		}
		return u, errors.New(msg)
	}
	return u, nil
}

func testTier(t *testing.T, cheap, strong *fakeGen, usdCap float64, callCap int) *Tier {
	t.Helper()
	return &Tier{
		Cheap: cheap, Strong: strong,
		ledger: LoadLedger(t.TempDir()),
		usdCap: usdCap, callCap: callCap, priced: true,
	}
}

func TestGenerateRoutesAndRecords(t *testing.T) {
	cheap := &fakeGen{name: "claude-haiku-4-5"}
	strong := &fakeGen{name: "claude-sonnet-5"}
	tier := testTier(t, cheap, strong, 1.0, 10)

	if _, err := tier.Generate(context.Background(), false, "s", "u", "n", nil, nil); err != nil {
		t.Fatal(err)
	}
	if cheap.calls != 1 || strong.calls != 0 {
		t.Errorf("cheap=%d strong=%d", cheap.calls, strong.calls)
	}
	if _, err := tier.Generate(context.Background(), true, "s", "u", "n", nil, nil); err != nil {
		t.Fatal(err)
	}
	if strong.calls != 1 {
		t.Errorf("strong not routed: %d", strong.calls)
	}

	d := tier.ledger.Days[day(time.Now().UTC())]
	if d.Calls != 2 || d.Prompt != 2000 {
		t.Errorf("ledger day = %+v", d)
	}
	// haiku 1000/500 → $0.0035; sonnet 1000/500 → $0.0105
	if d.USD < 0.013 || d.USD > 0.015 {
		t.Errorf("usd = %f", d.USD)
	}
}

func TestGenerateCallCap(t *testing.T) {
	cheap := &fakeGen{name: "m"}
	tier := testTier(t, cheap, cheap, 0, 2)
	ctx := context.Background()
	for range 2 {
		if _, err := tier.Generate(ctx, false, "s", "u", "n", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err := tier.Generate(ctx, false, "s", "u", "n", nil, nil)
	if !errors.Is(err, ErrCapped) {
		t.Fatalf("err = %v, want ErrCapped", err)
	}
	if cheap.calls != 2 {
		t.Errorf("capped call still hit the model: %d", cheap.calls)
	}
}

func TestGenerateRecordsFailedCalls(t *testing.T) {
	cheap := &fakeGen{name: "m", fail: true}
	tier := testTier(t, cheap, cheap, 0, 10)
	if _, err := tier.Generate(context.Background(), false, "s", "u", "n", nil, nil); err == nil {
		t.Fatal("want error")
	}
	if d := tier.ledger.Days[day(time.Now().UTC())]; d.Calls != 1 || d.Prompt != 1000 {
		t.Errorf("failed call not recorded: %+v", d)
	}
}

// A deterministic auth/config failure (logged-out CLI, bad key) must return
// ErrBackendUnavailable and NOT be folded into the daily cap — otherwise
// identical retries would drain daily_call_cap and stall learning after login.
func TestGenerateSkipsBackendUnavailable(t *testing.T) {
	cases := []string{
		"running claude -p: exit status 1 (Not logged in · Please run /login)",
		"anthropic: 401 authentication_error: invalid x-api-key",
		"openai: 401 invalid_api_key: Incorrect API key provided",
		"running codex exec: exit status 1 (login required; run codex login)",
	}
	for _, msg := range cases {
		cheap := &fakeGen{name: "m", fail: true, failMsg: msg}
		tier := testTier(t, cheap, cheap, 0, 40)
		_, err := tier.Generate(context.Background(), false, "s", "u", "n", nil, nil)
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Errorf("%q: err = %v, want ErrBackendUnavailable", msg, err)
		}
		if !IsStop(err) {
			t.Errorf("%q: IsStop = false, want true", msg)
		}
		if d := tier.ledger.Days[day(time.Now().UTC())]; d.Calls != 0 {
			t.Errorf("%q: backend-unavailable call was recorded: %+v", msg, d)
		}
	}
}

func TestEstimateUSDOpenAIModels(t *testing.T) {
	usage := llmgen.Usage{Prompt: 1_000_000, Completion: 1_000_000}
	for model, want := range map[string]float64{
		"gpt-5.6-luna (openai)":  7,
		"gpt-5.6-terra (openai)": 17.5,
		"gpt-5.6-sol (openai)":   35,
	} {
		if got := estimateUSD(model, usage); got != want {
			t.Errorf("estimateUSD(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestLedgerPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	l := LoadLedger(dir)
	now := time.Now().UTC()
	l.Record(now, llmgen.Usage{Prompt: 10, Completion: 5}, 0.02)
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	l2 := LoadLedger(dir)
	if d := l2.Days[day(now)]; d.Calls != 1 || d.USD != 0.02 {
		t.Errorf("reloaded = %+v", d)
	}
	// USD cap check.
	if err := l2.Allow(now, 0.01, 0); !errors.Is(err, ErrCapped) {
		t.Errorf("usd cap: %v", err)
	}
	if err := l2.Allow(now, 0.5, 0); err != nil {
		t.Errorf("under cap: %v", err)
	}
}

func TestResolveDisabledAndNone(t *testing.T) {
	for _, lc := range []config.LearnConfig{
		{Enabled: false},
		{Enabled: true, Provider: "none"},
	} {
		tier, desc, err := Resolve(lc, "http://localhost:11434", t.TempDir())
		if err != nil || tier != nil || desc == "" {
			t.Errorf("Resolve(%+v) = %v, %q, %v", lc, tier, desc, err)
		}
	}
}

func TestResolveExplicitErrors(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if _, _, err := Resolve(config.LearnConfig{Enabled: true, Provider: "anthropic"}, "", t.TempDir()); err == nil {
		t.Error("anthropic without key should error")
	}
	if _, _, err := Resolve(config.LearnConfig{Enabled: true, Provider: "openai"}, "", t.TempDir()); err == nil {
		t.Error("openai without key should error")
	}
	if _, _, err := Resolve(config.LearnConfig{
		Enabled: true, Provider: "ollama", CheapModel: "claude-haiku-4-5", StrongModel: "qwen3",
	}, "", t.TempDir()); err == nil {
		t.Error("ollama with claude model should error")
	}
	if _, _, err := Resolve(config.LearnConfig{Enabled: true, Provider: "bogus"}, "", t.TempDir()); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestResolveAutoWithoutBackends(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("PATH", t.TempDir()) // no claude binary
	tier, desc, err := Resolve(config.LearnConfig{Enabled: true, Provider: "auto"}, "", t.TempDir())
	if err != nil || tier != nil {
		t.Fatalf("auto must never error: tier=%v err=%v", tier, err)
	}
	if desc == "" {
		t.Error("want an options note")
	}
}

func TestProviderModelsReplaceCrossVendorDefaults(t *testing.T) {
	lc := config.LearnConfig{CheapModel: "claude-haiku-4-5", StrongModel: "claude-sonnet-5"}
	cheap, strong := providerModels(lc, "openai")
	if cheap != "gpt-5.6-luna" || strong != "gpt-5.6-terra" {
		t.Fatalf("openai models = %q, %q", cheap, strong)
	}
	lc = config.LearnConfig{CheapModel: "gpt-5.6-terra", StrongModel: "gpt-5.6-sol"}
	cheap, strong = providerModels(lc, "claude-cli")
	if cheap != "claude-haiku-4-5" || strong != "claude-sonnet-5" {
		t.Fatalf("claude models = %q, %q", cheap, strong)
	}
}
