package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/evalrun"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// evalTimeout bounds a whole suite run; one case is a single retrieval.
const evalTimeout = 5 * time.Minute

// Eval scores retrieval on a labeled suite against the store CULI_HOME points
// at. It exits non-zero when the hit rate falls below --min-hit, so it can gate
// CI or a retrieval change.
func Eval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	suitePath := fs.String("suite", "", "suite YAML file (required)")
	asJSON := fs.Bool("json", false, "print the full report as JSON")
	keywordOnly := fs.Bool("keyword-only", false, "skip the embedding arm even when Ollama is up")
	cwd := fs.String("cwd", "", "scope for cases without a cwd (default: a temp dir, global cards only)")
	minHit := fs.Float64("min-hit", 0, "fail when the hit rate is below this fraction (0-1)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	if *suitePath == "" {
		return fmt.Errorf("cli: usage: culi eval --suite FILE [--json] [--keyword-only] [--min-hit 0.8]")
	}
	suite, err := evalrun.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	base, cfg, err := loadBase()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout)
	defer cancel()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()

	if *cwd == "" {
		*cwd = os.TempDir() // not a git repo: only global cards are in scope
	}
	retrieve.ExtendLexicon(cfg.ExtraAcks, cfg.ExtraStopwords)
	r := &evalrun.Runner{
		Store: s, Model: cfg.Ollama.Model, Budget: cfg.PushBudget,
		PointerLines: cfg.PointerLines, DefaultCWD: *cwd,
	}
	if !*keywordOnly {
		r.Embedder = embed.NewOllama(cfg.Ollama.Endpoint, cfg.Ollama.Model, cfg.Ollama.KeepAlive)
	}
	rep, err := r.Run(ctx, suite)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fmt.Errorf("cli: encoding report: %w", err)
		}
	} else {
		printEvalReport(rep)
	}
	if rep.HitRate < *minHit {
		return fmt.Errorf("cli: hit rate %.2f is below --min-hit %.2f", rep.HitRate, *minHit)
	}
	return nil
}

func printEvalReport(rep evalrun.Report) {
	for _, res := range rep.Results {
		mark := "PASS"
		if !res.Pass {
			mark = "FAIL"
		}
		got := strings.Join(res.Injected, ", ")
		switch {
		case res.GateSkip != "":
			got = "(gate skip: " + res.GateSkip + ")"
		case got == "":
			got = "(nothing)"
		}
		want := strings.Join(res.Expect, ", ")
		if want == "" {
			want = "(nothing)"
		}
		fmt.Printf("%s  %-22s want %s\n      %-22s got  %s\n", mark, res.ID, want, "", got)
	}
	fmt.Printf("\nsuite %q, %s retrieval, %d cases (%d positive, %d abstain)\n",
		rep.Suite, rep.Mode, len(rep.Results), rep.Positive, rep.Abstain)
	fmt.Printf("  hit rate          %.0f%%  (at least one expected card injected)\n", 100*rep.HitRate)
	fmt.Printf("  MRR               %.2f\n", rep.MRR)
	fmt.Printf("  recall            %.0f%%\n", 100*rep.Recall)
	fmt.Printf("  precision         %.0f%%  (strict: unlabeled cards count as misses)\n", 100*rep.Precision)
	fmt.Printf("  abstain accuracy  %.0f%%  (nothing injected when nothing applies)\n", 100*rep.AbstainAccuracy)
	fmt.Printf("  tokens per case   %.0f\n", rep.MeanTokens)
	fmt.Printf("  latency           p50 %s, p95 %s (in-process, store already open)\n",
		rep.P50.Round(100*time.Microsecond), rep.P95.Round(100*time.Microsecond))
}
