package cli

import (
	"bufio"
	"context"
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
// the learn ledger.
func Stats(args []string) error {
	_ = args
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

	printRetrieval(ctx, s, base)
	printCards(ctx, s)
	printLearning(base)
	return nil
}

// printRetrieval covers injections + the gate's skip economics.
func printRetrieval(ctx context.Context, s *store.Store, base string) {
	fmt.Println("retrieval (injection log retention window, ~7 days)")
	sessions, err := s.SessionCount(ctx)
	if err == nil {
		fmt.Printf("  sessions           %d\n", sessions)
	}
	aggs, err := s.InjectionAggs(ctx)
	if err != nil {
		fmt.Printf("  injections         unavailable: %v\n", err)
	}
	byEvent := map[string]struct{ count, tokens int }{}
	for _, a := range aggs {
		e := byEvent[a.Event]
		e.count += a.Count
		e.tokens += a.Tokens
		byEvent[a.Event] = e
	}
	prompts := byEvent["user-prompt-submit"]
	starts := byEvent["session-start"]
	fmt.Printf("  prompt injections  %d cards, %d tokens\n", prompts.count, prompts.tokens)
	fmt.Printf("  baseline (start)   %d cards, %d tokens\n", starts.count, starts.tokens)
	for _, a := range aggs {
		fmt.Printf("    %-18s %-8s %4d cards %6d tokens\n", a.Event, a.Granularity, a.Count, a.Tokens)
	}

	if skips := gateSkips(base, 7*24*time.Hour); skips.total > 0 {
		var reasons []string
		for r, n := range skips.byReason {
			reasons = append(reasons, fmt.Sprintf("%s %d", r, n))
		}
		sort.Strings(reasons)
		fmt.Printf("  gate skips (7d)    %d prompts injected nothing for free — %s\n",
			skips.total, strings.Join(reasons, ", "))
	}
	fmt.Println()
}

// printCards covers the corpus and its utility signals.
func printCards(ctx context.Context, s *store.Store) {
	cards, err := s.AllCardsMeta(ctx)
	if err != nil {
		return
	}
	byType := map[string]int{}
	candidates, retired, corpusTok := 0, 0, 0
	for _, c := range cards {
		switch c.Status {
		case "candidate":
			candidates++
			continue // not injectable yet: keep the corpus line honest
		case "retired":
			retired++
			continue
		}
		byType[c.Type]++
		corpusTok += c.TokBody
	}
	var parts []string
	for _, t := range []string{"rule", "lesson", "style", "pattern", "skill", "agent"} {
		if byType[t] > 0 {
			parts = append(parts, fmt.Sprintf("%d %ss", byType[t], t))
		}
	}
	fmt.Printf("cards\n  corpus             %s (~%d body tokens)", strings.Join(parts, " · "), corpusTok)
	if candidates > 0 {
		fmt.Printf(" · %d candidate (culi review)", candidates)
	}
	if retired > 0 {
		fmt.Printf(" · %d retired", retired)
	}
	fmt.Println()

	stats, err := s.AllCardStats(ctx, time.Now().UTC())
	if err == nil {
		printTop(stats, "top pulled", func(cs store.CardStats) float64 { return 3*cs.Expanded + 5*cs.Referenced })
		printTop(stats, "noisy", func(cs store.CardStats) float64 { return cs.Downvoted })
	}
	printStale(ctx, s, cards, stats)
	fmt.Println()
}

// printStale lists live cards that were never injected in the retention
// window and show no decayed pull activity — the "does anyone still need
// this" governance view. Staleness is informational: a card can be dormant
// simply because its topic didn't come up.
func printStale(ctx context.Context, s *store.Store, cards []store.StoredCard, stats map[string]store.CardStats) {
	injected, err := s.InjectedCardIDs(ctx)
	if err != nil {
		return
	}
	var stale []string
	for _, c := range cards {
		if c.Status == "candidate" || c.Status == "retired" {
			continue // not expected to inject
		}
		if injected[c.ID] {
			continue
		}
		if cs, ok := stats[c.ID]; ok && cs.Expanded+cs.Referenced > 0.1 {
			continue // pulled via MCP even if never pushed
		}
		stale = append(stale, c.ID)
	}
	if len(stale) == 0 {
		return
	}
	sort.Strings(stale)
	shown := stale
	if len(shown) > 5 {
		shown = shown[:5]
	}
	fmt.Printf("  stale              %d live card(s) with no injection or pull in the window: %s",
		len(stale), strings.Join(shown, ", "))
	if len(stale) > len(shown) {
		fmt.Printf(", … (+%d)", len(stale)-len(shown))
	}
	fmt.Println()
}

func printTop(stats map[string]store.CardStats, label string, score func(store.CardStats) float64) {
	type row struct {
		id string
		v  float64
	}
	var rows []row
	for id, cs := range stats {
		if v := score(cs); v >= 0.5 {
			rows = append(rows, row{id, v})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].v > rows[j].v })
	if len(rows) > 3 {
		rows = rows[:3]
	}
	for i, r := range rows {
		tag := "  " + label
		if i > 0 {
			tag = strings.Repeat(" ", len(tag))
		}
		fmt.Printf("%-20s %s (%.1f)\n", tag, r.id, r.v)
	}
}

// printLearning covers the background pipelines' queues and spend.
func printLearning(base string) {
	fmt.Println("learning")
	if jobs, err := queue.List(config.InboxDir(base)); err == nil {
		fmt.Printf("  inbox              %d pending\n", len(jobs))
	}
	if n := countLines(filepath.Join(config.StateDir(base), "style_observations.jsonl")); n > 0 {
		fmt.Printf("  style observations %d\n", n)
	}
	ledger := llmtier.LoadLedger(config.StateDir(base))
	today := time.Now().UTC().Format("2006-01-02")
	var calls7, callsToday int
	var usd7, usdToday float64
	cutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	for day, d := range ledger.Days {
		if day == today {
			callsToday, usdToday = d.Calls, d.USD
		}
		if day >= cutoff {
			calls7 += d.Calls
			usd7 += d.USD
		}
	}
	fmt.Printf("  spend today        %d calls, $%.2f\n", callsToday, usdToday)
	fmt.Printf("  spend last 7d      %d calls, $%.2f\n", calls7, usd7)
}

// gateSkipCounts tallies "gate skip (reason)" hook-log lines in the window.
type gateSkipCounts struct {
	total    int
	byReason map[string]int
}

// gateSkips scrapes the hook log — skips are deliberately not DB state (the
// gate must stay write-free), so stats reads the breadcrumbs.
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
