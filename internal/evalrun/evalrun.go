// Package evalrun measures retrieval quality. It runs labeled prompts through
// the same gate → retrieve → pack funnel the prompt hook uses and scores the
// injected cards against the expected ones, so a change to ranking, gating or
// packing shows up as a number instead of a feeling.
package evalrun

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/pack"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// Case is one labeled prompt. An empty Expect makes it an abstain case: the
// correct outcome is that nothing is injected.
type Case struct {
	ID     string   `yaml:"id" json:"id"`
	Prompt string   `yaml:"prompt" json:"prompt"`
	CWD    string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Expect []string `yaml:"expect" json:"expect"`
}

// Suite is a named list of cases, loaded from YAML.
type Suite struct {
	Name  string `yaml:"name"`
	Cases []Case `yaml:"cases"`
}

// LoadSuite reads and validates a suite file.
func LoadSuite(path string) (Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, fmt.Errorf("evalrun: reading suite: %w", err)
	}
	var s Suite
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return Suite{}, fmt.Errorf("evalrun: parsing %s: %w", path, err)
	}
	if len(s.Cases) == 0 {
		return Suite{}, fmt.Errorf("evalrun: %s has no cases", path)
	}
	seen := make(map[string]bool, len(s.Cases))
	for i, c := range s.Cases {
		if c.ID == "" || c.Prompt == "" {
			return Suite{}, fmt.Errorf("evalrun: case %d needs an id and a prompt", i+1)
		}
		if seen[c.ID] {
			return Suite{}, fmt.Errorf("evalrun: duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
	}
	return s, nil
}

// Runner holds what the funnel needs. Embedder nil means keyword-only.
type Runner struct {
	Store        *store.Store
	Embedder     embed.Embedder
	Model        string
	Budget       int
	PointerLines int
	DefaultCWD   string // scope for cases without a cwd
}

// Result is the outcome of one case.
type Result struct {
	Case
	Injected  []string      `json:"injected"`
	Tokens    int           `json:"tokens"`
	GateSkip  string        `json:"gate_skip,omitempty"`
	Pass      bool          `json:"pass"`
	Rank      int           `json:"rank,omitempty"` // 1-based rank of the first expected card; 0 if absent
	Recall    float64       `json:"recall"`
	Precision float64       `json:"precision"`
	Latency   time.Duration `json:"latency_ns"`
}

// Report aggregates a suite run. Rates are fractions in [0, 1]. Precision is
// strict: an injected card that is useful but not labeled counts against it.
type Report struct {
	Suite           string        `json:"suite"`
	Mode            string        `json:"mode"`
	Results         []Result      `json:"results"`
	Positive        int           `json:"positive_cases"`
	Abstain         int           `json:"abstain_cases"`
	HitRate         float64       `json:"hit_rate"`
	MRR             float64       `json:"mrr"`
	Recall          float64       `json:"recall"`
	Precision       float64       `json:"precision"`
	AbstainAccuracy float64       `json:"abstain_accuracy"`
	MeanTokens      float64       `json:"mean_tokens"`
	P50             time.Duration `json:"p50_ns"`
	P95             time.Duration `json:"p95_ns"`
}

// Run scores every case in order.
func (r *Runner) Run(ctx context.Context, s Suite) (Report, error) {
	rep := Report{Suite: s.Name, Mode: "keyword"}
	if r.Embedder != nil {
		rep.Mode = "hybrid"
	}
	rep.Results = make([]Result, 0, len(s.Cases))
	for _, c := range s.Cases {
		if err := ctx.Err(); err != nil {
			return rep, fmt.Errorf("evalrun: %w", err)
		}
		res, err := r.runCase(ctx, c)
		if err != nil {
			return rep, fmt.Errorf("evalrun: case %s: %w", c.ID, err)
		}
		rep.Results = append(rep.Results, res)
	}
	summarize(&rep)
	return rep, nil
}

func (r *Runner) runCase(ctx context.Context, c Case) (Result, error) {
	res := Result{Case: c}
	start := time.Now()

	gate := retrieve.Gate(c.Prompt, "")
	if gate.Skip {
		res.GateSkip = gate.Reason
		score(&res)
		res.Latency = time.Since(start)
		return res, nil
	}
	cwd := c.CWD
	if cwd == "" {
		cwd = r.DefaultCWD
	}
	ret := &retrieve.Retriever{Store: r.Store, Embedder: r.Embedder, Model: r.Model}
	cands, err := ret.Retrieve(ctx, gate.Query, retrieve.DetectScope(cwd))
	if err != nil {
		return res, err
	}
	inj, err := pack.Pack(cands, nil, r.Budget, r.PointerLines, r.loadBodies(ctx))
	if err != nil {
		return res, err
	}
	res.Tokens = inj.Tokens
	res.Injected = make([]string, 0, len(inj.Items))
	for _, it := range inj.Items {
		res.Injected = append(res.Injected, it.CardID)
	}
	score(&res)
	res.Latency = time.Since(start)
	return res, nil
}

func (r *Runner) loadBodies(ctx context.Context) pack.BodyLoader {
	return func(rowids []int64) (map[int64]string, error) {
		cards, err := r.Store.CardsByRowid(ctx, rowids)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]string, len(cards))
		for _, c := range cards {
			out[c.Rowid] = c.Body
		}
		return out, nil
	}
}

// score fills Pass, Rank, Recall and Precision from Expect and Injected.
func score(res *Result) {
	if len(res.Expect) == 0 {
		res.Pass = len(res.Injected) == 0
		return
	}
	relevant := 0
	for i, id := range res.Injected {
		if slices.Contains(res.Expect, id) {
			relevant++
			if res.Rank == 0 {
				res.Rank = i + 1
			}
		}
	}
	res.Pass = relevant > 0
	res.Recall = float64(relevant) / float64(len(res.Expect))
	if len(res.Injected) > 0 {
		res.Precision = float64(relevant) / float64(len(res.Injected))
	}
}

func summarize(rep *Report) {
	var hits, abstainOK, tokens int
	var rr, recall, precision float64
	lat := make([]time.Duration, 0, len(rep.Results))
	for _, res := range rep.Results {
		lat = append(lat, res.Latency)
		tokens += res.Tokens
		if len(res.Expect) == 0 {
			rep.Abstain++
			if res.Pass {
				abstainOK++
			}
			continue
		}
		rep.Positive++
		if res.Pass {
			hits++
		}
		if res.Rank > 0 {
			rr += 1 / float64(res.Rank)
		}
		recall += res.Recall
		precision += res.Precision
	}
	if rep.Positive > 0 {
		n := float64(rep.Positive)
		rep.HitRate, rep.MRR = float64(hits)/n, rr/n
		rep.Recall, rep.Precision = recall/n, precision/n
	}
	if rep.Abstain > 0 {
		rep.AbstainAccuracy = float64(abstainOK) / float64(rep.Abstain)
	}
	if n := len(rep.Results); n > 0 {
		rep.MeanTokens = float64(tokens) / float64(n)
		sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
		rep.P50 = lat[n/2]
		rep.P95 = lat[min(n-1, n*95/100)]
	}
}
