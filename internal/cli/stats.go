package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/store"
)

// Stats prints the token-accounting and learning report (plan §10). All
// numbers are best-effort views over disposable state: the injection log is
// retention-bounded (~7 days), skip counts come from the hook log, spend from
// the learn ledger. --json emits the same report machine-readably for
// external monitoring (field names are snake_case; treat the schema as
// evolving, not a contract).
func Stats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("cli: %w", err)
	}
	base, _, err := loadBase()
	if err != nil {
		return err
	}
	ctx := context.Background()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		return err
	}
	defer s.Close()

	rep := gather(ctx, s, base)
	if *asJSON {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return fmt.Errorf("cli: marshaling stats: %w", err)
		}
		fmt.Println(string(raw))
		return nil
	}
	renderText(rep)
	return nil
}

// report is the full stats payload, shared by the text and JSON renderers.
type report struct {
	Retrieval retrievalReport `json:"retrieval"`
	Cards     cardsReport     `json:"cards"`
	Learning  learningReport  `json:"learning"`
}

type retrievalReport struct {
	Sessions       int                  `json:"sessions"`
	PromptCards    int                  `json:"prompt_cards"`
	PromptTokens   int                  `json:"prompt_tokens"`
	BaselineCards  int                  `json:"baseline_cards"`
	BaselineTokens int                  `json:"baseline_tokens"`
	Buckets        []store.InjectionAgg `json:"buckets,omitempty"`
	GateSkipTotal  int                  `json:"gate_skip_total"`
	GateSkips      map[string]int       `json:"gate_skips,omitempty"`
	Counterfactual counterfactual       `json:"counterfactual"`
}

// counterfactual estimates what the retention window would have cost if,
// instead of culi's budgeted retrieval, every live card body were loaded at
// every session start (the CLAUDE.md-dump baseline culi replaces).
type counterfactual struct {
	DumpTokens   int     `json:"dump_everything_tokens"`
	Injected     int     `json:"injected_tokens"`
	SavedTokens  int     `json:"saved_tokens"`
	SavedPercent float64 `json:"saved_percent"`
}

type cardsReport struct {
	ByType           map[string]int `json:"by_type"`
	Candidates       int            `json:"candidates"`
	Retired          int            `json:"retired"`
	CorpusBodyTokens int            `json:"corpus_body_tokens"`
	TopPulled        []cardScore    `json:"top_pulled,omitempty"`
	Noisy            []cardScore    `json:"noisy,omitempty"`
	Stale            []string       `json:"stale,omitempty"`
}

type cardScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

type learningReport struct {
	InboxPending      int   `json:"inbox_pending"`
	FailedJobs        int   `json:"failed_jobs"`
	StyleObservations int   `json:"style_observations"`
	SpendToday        spend `json:"spend_today"`
	Spend7d           spend `json:"spend_7d"`
}

type spend struct {
	Calls int     `json:"calls"`
	USD   float64 `json:"usd"`
}

// gather assembles the report. Every sub-collector is best-effort — a partial
// report beats a failed one.
func gather(ctx context.Context, s *store.Store, base string) report {
	var rep report
	rep.Retrieval = gatherRetrieval(ctx, s, base)
	rep.Cards = gatherCards(ctx, s)
	rep.Learning = gatherLearning(base)
	rep.Retrieval.Counterfactual = computeCounterfactual(
		rep.Retrieval.Sessions, rep.Cards.CorpusBodyTokens,
		rep.Retrieval.PromptTokens+rep.Retrieval.BaselineTokens)
	return rep
}

func gatherRetrieval(ctx context.Context, s *store.Store, base string) retrievalReport {
	var r retrievalReport
	r.Sessions, _ = s.SessionCount(ctx)
	aggs, _ := s.InjectionAggs(ctx)
	r.Buckets = aggs
	for _, a := range aggs {
		switch a.Event {
		case "user-prompt-submit":
			r.PromptCards += a.Count
			r.PromptTokens += a.Tokens
		case "session-start":
			r.BaselineCards += a.Count
			r.BaselineTokens += a.Tokens
		}
	}
	skips := gateSkips(base, 7*24*time.Hour)
	r.GateSkipTotal, r.GateSkips = skips.total, skips.byReason
	return r
}

// computeCounterfactual is the "is culi paying for itself" number: what the
// window's sessions would have cost under load-everything-every-session,
// versus what was actually injected. An estimate, not billing — chars/4
// token estimates on both sides, and the dump baseline assumes one full load
// per session.
func computeCounterfactual(sessions, corpusBodyTok, injectedTok int) counterfactual {
	cf := counterfactual{
		DumpTokens: sessions * corpusBodyTok,
		Injected:   injectedTok,
	}
	cf.SavedTokens = cf.DumpTokens - cf.Injected
	if cf.DumpTokens > 0 {
		cf.SavedPercent = 100 * float64(cf.SavedTokens) / float64(cf.DumpTokens)
	}
	return cf
}

func gatherCards(ctx context.Context, s *store.Store) cardsReport {
	r := cardsReport{ByType: map[string]int{}}
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return r
	}
	for _, c := range cards {
		switch c.Status {
		case "candidate":
			r.Candidates++
			continue
		case "retired":
			r.Retired++
			continue
		}
		r.ByType[c.Type]++
		r.CorpusBodyTokens += c.TokBody
	}
	stats, err := s.AllCardStats(ctx, time.Now().UTC())
	if err == nil {
		r.TopPulled = topScores(stats, func(cs store.CardStats) float64 { return 3*cs.Expanded + 5*cs.Referenced })
		r.Noisy = topScores(stats, func(cs store.CardStats) float64 { return cs.Downvoted })
	}
	r.Stale = staleCards(ctx, s, cards, stats)
	return r
}

func topScores(stats map[string]store.CardStats, score func(store.CardStats) float64) []cardScore {
	var rows []cardScore
	for id, cs := range stats {
		if v := score(cs); v >= 0.5 {
			rows = append(rows, cardScore{ID: id, Score: v})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	if len(rows) > 3 {
		rows = rows[:3]
	}
	return rows
}

// staleCards lists live cards that were never injected in the retention
// window and show no decayed pull activity — informational: dormant can just
// mean the topic didn't come up.
func staleCards(ctx context.Context, s *store.Store, cards []store.StoredCard, stats map[string]store.CardStats) []string {
	injected, err := s.InjectedCardIDs(ctx)
	if err != nil {
		return nil
	}
	var stale []string
	for _, c := range cards {
		if c.Status == "candidate" || c.Status == "retired" {
			continue
		}
		if injected[c.ID] {
			continue
		}
		if cs, ok := stats[c.ID]; ok && cs.Expanded+cs.Referenced > 0.1 {
			continue
		}
		stale = append(stale, c.ID)
	}
	sort.Strings(stale)
	return stale
}

func gatherLearning(base string) learningReport {
	var r learningReport
	if jobs, err := queue.List(config.InboxDir(base)); err == nil {
		r.InboxPending = len(jobs)
	}
	if entries, err := os.ReadDir(filepath.Join(config.InboxDir(base), "failed")); err == nil {
		for _, e := range entries {
			// Only parked job files count — a stray .DS_Store must not trip
			// the loud FAILED JOBS line.
			if !e.IsDir() && strings.Contains(e.Name(), ".json") {
				r.FailedJobs++
			}
		}
	}
	r.StyleObservations = countLines(filepath.Join(config.StateDir(base), "style_observations.jsonl"))
	ledger := llmtier.LoadLedger(config.StateDir(base))
	today := time.Now().UTC().Format("2006-01-02")
	cutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	for day, d := range ledger.Days {
		if day == today {
			r.SpendToday = spend{Calls: d.Calls, USD: d.USD}
		}
		if day >= cutoff {
			r.Spend7d.Calls += d.Calls
			r.Spend7d.USD += d.USD
		}
	}
	return r
}

// --- text renderer ---

func renderText(rep report) {
	r := rep.Retrieval
	fmt.Println("retrieval (injection log retention window, ~7 days)")
	fmt.Printf("  sessions           %d\n", r.Sessions)
	fmt.Printf("  prompt injections  %d cards, %d tokens\n", r.PromptCards, r.PromptTokens)
	fmt.Printf("  baseline (start)   %d cards, %d tokens\n", r.BaselineCards, r.BaselineTokens)
	for _, a := range r.Buckets {
		fmt.Printf("    %-18s %-8s %4d cards %6d tokens\n", a.Event, a.Granularity, a.Count, a.Tokens)
	}
	if r.GateSkipTotal > 0 {
		var reasons []string
		for reason, n := range r.GateSkips {
			reasons = append(reasons, fmt.Sprintf("%s %d", reason, n))
		}
		sort.Strings(reasons)
		fmt.Printf("  gate skips (7d)    %d prompts injected nothing for free — %s\n",
			r.GateSkipTotal, strings.Join(reasons, ", "))
	}
	if cf := r.Counterfactual; cf.DumpTokens > 0 {
		fmt.Printf("  savings estimate   injected %d tokens vs ~%d if every live card loaded each session — saved ~%d (%.0f%%)\n",
			cf.Injected, cf.DumpTokens, cf.SavedTokens, cf.SavedPercent)
	}
	fmt.Println()

	c := rep.Cards
	var parts []string
	for _, t := range []string{"rule", "lesson", "style", "pattern", "skill", "agent"} {
		if c.ByType[t] > 0 {
			parts = append(parts, fmt.Sprintf("%d %ss", c.ByType[t], t))
		}
	}
	fmt.Printf("cards\n  corpus             %s (~%d body tokens)", strings.Join(parts, " · "), c.CorpusBodyTokens)
	if c.Candidates > 0 {
		fmt.Printf(" · %d candidate (culi review)", c.Candidates)
	}
	if c.Retired > 0 {
		fmt.Printf(" · %d retired", c.Retired)
	}
	fmt.Println()
	printScores("top pulled", c.TopPulled)
	printScores("noisy", c.Noisy)
	if len(c.Stale) > 0 {
		shown := c.Stale
		if len(shown) > 5 {
			shown = shown[:5]
		}
		fmt.Printf("  stale              %d live card(s) with no injection or pull in the window: %s",
			len(c.Stale), strings.Join(shown, ", "))
		if len(c.Stale) > len(shown) {
			fmt.Printf(", … (+%d)", len(c.Stale)-len(shown))
		}
		fmt.Println()
	}
	fmt.Println()

	l := rep.Learning
	fmt.Println("learning")
	fmt.Printf("  inbox              %d pending\n", l.InboxPending)
	if l.FailedJobs > 0 {
		fmt.Printf("  FAILED JOBS        %d in inbox/failed — inspect them, these are bugs\n", l.FailedJobs)
	}
	if l.StyleObservations > 0 {
		fmt.Printf("  style observations %d\n", l.StyleObservations)
	}
	fmt.Printf("  spend today        %d calls, $%.2f\n", l.SpendToday.Calls, l.SpendToday.USD)
	fmt.Printf("  spend last 7d      %d calls, $%.2f\n", l.Spend7d.Calls, l.Spend7d.USD)
}

func printScores(label string, rows []cardScore) {
	for i, r := range rows {
		tag := "  " + label
		if i > 0 {
			tag = strings.Repeat(" ", len(tag))
		}
		fmt.Printf("%-20s %s (%.1f)\n", tag, r.ID, r.Score)
	}
}

// --- log scraping ---

// gateSkipCounts tallies "gate skip (reason)" hook-log lines in the window.
type gateSkipCounts struct {
	total    int
	byReason map[string]int
}

// gateSkips scrapes the hook log — skips are deliberately not DB state (the
// gate must stay write-free), so stats reads the breadcrumbs. Scanner errors
// (a pathological >1MB line) truncate the tally silently: best-effort.
func gateSkips(base string, window time.Duration) gateSkipCounts {
	out := gateSkipCounts{byReason: map[string]int{}}
	f, err := os.Open(filepath.Join(config.LogDir(base), "hook.log"))
	if err != nil {
		return out
	}
	defer f.Close()
	cutoff := time.Now().UTC().Add(-window)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		ts, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, ts); err != nil || t.Before(cutoff) {
			continue
		}
		if reason, ok := skipReason(rest); ok {
			out.total++
			out.byReason[reason]++
		}
	}
	return out
}

// skipReason parses `gate skip (reason) session=...` breadcrumbs.
func skipReason(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "gate skip (")
	if !ok {
		return "", false
	}
	if i := strings.IndexByte(rest, ')'); i > 0 {
		return rest[:i], true
	}
	return "", false
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) > 0 {
			n++
		}
	}
	return n
}
