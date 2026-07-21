package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/store"
)

// ---------- payload types (JSON field names match the frontend view models) ----------

type statusPayload struct {
	Cards         int     `json:"cards"`
	SavedPct      string  `json:"savedPct"`
	ToReview      int     `json:"toReview"`
	ToLearn       int     `json:"toLearn"` // queued transcripts waiting to be mined
	Addr          string  `json:"addr"`
	SpendToday    float64 `json:"spendToday"`
	SpendCap      float64 `json:"spendCap"`
	FailedJobs    int     `json:"failedJobs"`
	OllamaOffline bool    `json:"ollamaOffline"`
	ServeDown     bool    `json:"serveDown"`
}

type cfPayload struct {
	Injected string `json:"injected"`
	Sessions string `json:"sessions"`
	VsAll    string `json:"vsAll"`
}
type tile struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Sub   string `json:"sub"`
	Color string `json:"color"`
}
type granPayload struct {
	Body    int `json:"body"`
	Summary int `json:"summary"`
	Hook    int `json:"hook"`
}
type failedJob struct {
	Kind   string `json:"kind"`
	At     string `json:"at"`
	Reason string `json:"reason"`
}
type noisyItem struct {
	ID    string `json:"id"`
	Short string `json:"short"` // 4-hex id for the KB deep-link ("" if card gone)
	Name  string `json:"name"`
	Score string `json:"score"`
}
type staleItem struct {
	Name  string `json:"name"`
	Short string `json:"short"` // 4-hex id for the KB deep-link ("" if card gone)
	Last  string `json:"last"`
}
type overviewPayload struct {
	SavedPct    string      `json:"savedPct"`
	Trend       []int       `json:"trend"`
	CF          cfPayload   `json:"cf"`
	Tiles       []tile      `json:"tiles"`
	Granularity granPayload `json:"granularity"`
	FailedJobs  []failedJob `json:"failedJobs"`
	Noisy       []noisyItem `json:"noisy"`
	StaleHeader string      `json:"staleHeader"`
	StaleMore   string      `json:"staleMore"`
	Stale       []staleItem `json:"stale"`
}

type supersedes struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type candidatePayload struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Title        string      `json:"title"`
	Summary      string      `json:"summary"`
	ScopeLabels  []string    `json:"scopeLabels"`
	Observations int         `json:"observations"`
	Keywords     []string    `json:"keywords"`
	Source       string      `json:"source"`
	Model        string      `json:"model"`
	Created      string      `json:"created"`
	Ago          string      `json:"ago"`
	Supersedes   *supersedes `json:"supersedes"`
	Evidence     string      `json:"evidence"`
	Body         string      `json:"body"`
}

type cardListItem struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	ScopeLabels []string `json:"scopeLabels"`
	Baseline    bool     `json:"baseline"`
	Injected    int      `json:"injected"`
	Spark       []int    `json:"spark"`
	Key         string   `json:"key"`
	Summary     string   `json:"summary"`  // for client-side search
	Triggers    []string `json:"triggers"` // keywords + aliases, for search
	Stale       bool     `json:"stale"`    // never pulled in the retention window (KB "Stale" facet)
}

type usageTile struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Color string `json:"color"`
}
type cardDetailPayload struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Title       string      `json:"title"`
	Status      string      `json:"status"`
	ScopeLabels []string    `json:"scopeLabels"`
	Baseline    bool        `json:"baseline"`
	Key         string      `json:"key"`
	Body        string      `json:"body"`
	Source      string      `json:"source"`
	MergedFrom  string      `json:"mergedFrom"`
	Model       string      `json:"model"`
	Hash        string      `json:"hash"`
	Usage       []usageTile `json:"usage"`
	Triggers    []string    `json:"triggers"`
	Keywords    []string    `json:"keywords"` // trigger keywords only, for the edit form
	Editable    bool        `json:"editable"` // culi-authored → safe to rewrite via the console
	History     []histEntry `json:"history"`
}

// injCard is one card in an injection event's full breakdown — the exact
// card that was applied, at what granularity, for how many tokens. Short is
// the 4-hex id used to deep-link into the Knowledge Base ("" if the card has
// since been removed from the corpus).
type injCard struct {
	ID    string `json:"id"`
	Short string `json:"short"`
	Gran  string `json:"gran"`
	Tok   int    `json:"tok"`
}
type sessionEvent struct {
	At    string    `json:"at"`
	Ev    string    `json:"ev"`
	Cards string    `json:"cards"`
	Gran  string    `json:"gran"`
	Tok   string    `json:"tok"`
	List  []injCard `json:"list"` // full per-card breakdown, revealed on expand
}
type sessionPayload struct {
	ID       string         `json:"id"`
	Repo     string         `json:"repo"`     // basename of the working dir ("" when unknown)
	RepoPath string         `json:"repoPath"` // full cwd, for the tooltip
	Time     string         `json:"time"`
	Tokens   string         `json:"tokens"`
	Events   []sessionEvent `json:"events"`
}

// injectionsPayload wraps the Activity injections list with the distinct repo
// labels present in the recent window, so the client can render a repo filter
// whose options stay stable regardless of the active filter.
type injectionsPayload struct {
	Repos    []string         `json:"repos"`
	Sessions []sessionPayload `json:"sessions"`
}

type runPayload struct {
	Date       string `json:"date"`
	Mined      string `json:"mined"`
	Clean      string `json:"clean"`
	Candidates string `json:"candidates"`
	Patterns   string `json:"patterns"`
	Spend      string `json:"spend"`
	Dot        string `json:"dot"`
	Failed     bool   `json:"failed"`
}

type setItem struct {
	Key   string `json:"key"`
	Desc  string `json:"desc"`
	Value string `json:"value"`
	Width string `json:"width"`
}
type setGroup struct {
	Title string    `json:"title"`
	Items []setItem `json:"items"`
}
type settingsPayload struct {
	SpendToday float64    `json:"spendToday"`
	SpendCap   float64    `json:"spendCap"`
	Groups     []setGroup `json:"groups"`
}

// statsView is the subset of the `culi stats` JSON report the console consumes.
// serve re-marshals the opaque report value through JSON so it need not import
// (and cycle with) the cli package that owns the concrete report type.
type statsView struct {
	Retrieval struct {
		Sessions      int `json:"sessions"`
		GateSkipTotal int `json:"gate_skip_total"`
		Buckets       []struct {
			Event       string `json:"event"`
			Granularity string `json:"granularity"`
			Count       int    `json:"count"`
			Tokens      int    `json:"tokens"`
		} `json:"buckets"`
		Counterfactual struct {
			DumpTokens   int     `json:"dump_everything_tokens"`
			Injected     int     `json:"injected_tokens"`
			SavedPercent float64 `json:"saved_percent"`
		} `json:"counterfactual"`
	} `json:"retrieval"`
	Cards struct {
		ByType     map[string]int `json:"by_type"`
		Candidates int            `json:"candidates"`
		Noisy      []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"noisy"`
		Stale []string `json:"stale"`
	} `json:"cards"`
	Learning struct {
		InboxPending int `json:"inbox_pending"`
		FailedJobs   int `json:"failed_jobs"`
		SpendToday   struct {
			Calls int     `json:"calls"`
			USD   float64 `json:"usd"`
		} `json:"spend_today"`
	} `json:"learning"`
}

func (s *server) stats(ctx context.Context) statsView {
	var sv statsView
	if s.statsFn == nil {
		return sv
	}
	raw, err := json.Marshal(s.statsFn(ctx))
	if err != nil {
		return sv
	}
	_ = json.Unmarshal(raw, &sv)
	return sv
}

// ---------- builders ----------

func (s *server) buildStatus(ctx context.Context) statusPayload {
	sv := s.stats(ctx)
	metas, _ := s.store.AllCardsMeta(ctx)
	toReview := 0
	for _, m := range metas {
		if m.Status == "candidate" {
			toReview++
		}
	}
	return statusPayload{
		Cards:         len(metas),
		SavedPct:      fmtPct(sv.Retrieval.Counterfactual.DumpTokens, sv.Retrieval.Counterfactual.SavedPercent),
		ToReview:      toReview,
		ToLearn:       sv.Learning.InboxPending,
		Addr:          s.addr,
		SpendToday:    sv.Learning.SpendToday.USD,
		SpendCap:      s.config().Learn.DailyUSDCap,
		FailedJobs:    sv.Learning.FailedJobs,
		OllamaOffline: s.ollamaOffline(ctx),
	}
}

func (s *server) ollamaOffline(ctx context.Context) bool {
	v, err := s.store.MetaGet(ctx, "embed_break_until")
	if err != nil || v == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return false
	}
	return time.Now().Before(t)
}

func (s *server) buildOverview(ctx context.Context) overviewPayload {
	sv := s.stats(ctx)
	metas, _ := s.store.AllCardsMeta(ctx)
	cf := sv.Retrieval.Counterfactual

	// Map full card id → short id so the noisy/stale rows can deep-link into the KB.
	idToShort := make(map[string]string, len(metas))
	for _, m := range metas {
		idToShort[m.ID] = m.ShortID
	}

	trend := normalizeTrend(mustDaily(s.store.DailyTokens(ctx, 12)))

	tiles := []tile{
		{Label: "Cards", Value: itoa(len(metas)), Sub: typeLettersSub(sv.Cards.ByType), Color: "#f2f2f5"},
		{Label: "To review", Value: itoa(sv.Cards.Candidates), Sub: "candidates queued", Color: "#e6ac5c"},
		{Label: "Gate skips", Value: itoa(sv.Retrieval.GateSkipTotal), Sub: "last 7d", Color: "#5fe0d3"},
		{Label: "Spend today", Value: fmtUSD(sv.Learning.SpendToday.USD), Sub: itoa(sv.Learning.SpendToday.Calls) + " calls", Color: "#f2f2f5"},
	}

	var failed []failedJob
	if sv.Learning.FailedJobs > 0 {
		failed = []failedJob{{Kind: "learn run", At: "", Reason: "a queued learn job failed — retry it from the inbox"}}
	}

	var noisy []noisyItem
	for i, n := range sv.Cards.Noisy {
		if i >= 3 {
			break
		}
		noisy = append(noisy, noisyItem{ID: n.ID, Short: idToShort[n.ID], Name: n.ID, Score: fmt.Sprintf("%.1f", n.Score)})
	}
	var stale []staleItem
	for i, id := range sv.Cards.Stale {
		if i >= 3 {
			break
		}
		stale = append(stale, staleItem{Name: id, Short: idToShort[id], Last: "—"})
	}
	staleMore := ""
	if len(sv.Cards.Stale) > 3 {
		staleMore = "+ " + itoa(len(sv.Cards.Stale)-3) + " more"
	}

	return overviewPayload{
		SavedPct:    fmtPct(cf.DumpTokens, cf.SavedPercent),
		Trend:       trend,
		CF:          cfPayload{Injected: commas(cf.Injected), Sessions: itoa(sv.Retrieval.Sessions), VsAll: humanizeTokens(cf.DumpTokens)},
		Tiles:       tiles,
		Granularity: granFromBuckets(sv),
		FailedJobs:  failed,
		Noisy:       noisy,
		StaleHeader: itoa(len(sv.Cards.Stale)) + " never pulled (30d)",
		StaleMore:   staleMore,
		Stale:       stale,
	}
}

func (s *server) buildCandidates(ctx context.Context) []candidatePayload {
	metas, _ := s.store.AllCardsMeta(ctx)
	sort.Slice(metas, func(a, b int) bool { return metas[a].Rowid > metas[b].Rowid })
	out := []candidatePayload{}
	for _, m := range metas {
		if m.Status != "candidate" {
			continue
		}
		card, err := knowledge.ReadCard(s.kdir, m.Path)
		if err != nil {
			continue
		}
		evidence, cleanBody := extractEvidence(card.Body)
		var sup *supersedes
		if card.Supersedes != "" {
			title := card.Supersedes
			if sc, e := s.store.CardByID(ctx, card.Supersedes); e == nil {
				title = sc.Title
			}
			sup = &supersedes{ID: knowledge.ShortID(card.Supersedes), Title: title}
		}
		src, model := provenanceOf(card)
		created, ago := fileTimes(s.kdir, m.Path)
		out = append(out, candidatePayload{
			ID:           m.ShortID,
			Type:         card.Type,
			Title:        card.Title,
			Summary:      card.Summary,
			ScopeLabels:  card.Scopes,
			Observations: card.Observations,
			Keywords:     card.Triggers.Keywords,
			Source:       src,
			Model:        model,
			Created:      created,
			Ago:          ago,
			Supersedes:   sup,
			Evidence:     evidence,
			Body:         renderMarkdown(cleanBody),
		})
	}
	return out
}

func (s *server) buildCards(ctx context.Context) []cardListItem {
	metas, _ := s.store.AllCardsMeta(ctx)
	stats, _ := s.store.AllCardStats(ctx, time.Now())
	injected, _ := s.store.InjectedCardIDs(ctx)
	out := make([]cardListItem, 0, len(metas))
	for _, m := range metas {
		triggers := append([]string{}, m.Triggers.Keywords...)
		triggers = append(triggers, m.Aliases...)
		out = append(out, cardListItem{
			ID:          m.ShortID,
			Type:        m.Type,
			Title:       m.Title,
			Status:      normStatus(m.Status),
			ScopeLabels: m.Scopes,
			Baseline:    m.Baseline,
			Injected:    round(stats[m.ID].Injected),
			Spark:       nil, // no per-card time series in the index; sparkline omitted
			Key:         m.Key,
			Summary:     m.Summary,
			Triggers:    triggers,
			Stale:       isStale(m.Status, injected[m.ID], stats[m.ID]),
		})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Injected > out[b].Injected })
	return out
}

func (s *server) buildCardDetail(ctx context.Context, id string) (cardDetailPayload, bool) {
	sc, err := s.store.CardByID(ctx, id)
	if err != nil {
		return cardDetailPayload{}, false
	}
	card := sc.Card
	if fc, e := knowledge.ReadCard(s.kdir, sc.Path); e == nil {
		card = fc // file is truth: provenance/aliases live only on disk
	}
	stats, _ := s.store.AllCardStats(ctx, time.Now())
	cs := stats[sc.ID]
	src, model := provenanceOf(card)
	merged, hash := "—", "—"
	if card.Provenance != nil {
		if len(card.Provenance.MergedFrom) > 0 {
			merged = strings.Join(card.Provenance.MergedFrom, ", ")
		}
		if card.Provenance.SourceHash != "" {
			hash = card.Provenance.SourceHash
		}
	}
	triggers := append([]string{}, card.Triggers.Keywords...)
	triggers = append(triggers, card.Triggers.Globs...)
	triggers = append(triggers, card.Aliases...)

	return cardDetailPayload{
		ID:          sc.ShortID,
		Type:        card.Type,
		Title:       card.Title,
		Status:      normStatus(card.Status),
		ScopeLabels: card.Scopes,
		Baseline:    card.Baseline,
		Key:         card.Key,
		Body:        renderMarkdown(card.Body),
		Source:      src,
		MergedFrom:  merged,
		Model:       model,
		Hash:        hash,
		Usage: []usageTile{
			{Label: "injected", Value: itoa(round(cs.Injected)), Color: "#47d1c4"},
			{Label: "expanded", Value: itoa(round(cs.Expanded)), Color: "#e4e4e8"},
			{Label: "referenced", Value: itoa(round(cs.Referenced)), Color: "#e4e4e8"},
			{Label: "downvoted", Value: itoa(round(cs.Downvoted)), Color: "#ff8079"},
		},
		Triggers: triggers,
		Keywords: card.Triggers.Keywords,
		Editable: culiEditable(card),
		History:  cardHistory(ctx, s.kdir, filepath.FromSlash(sc.Path), 6),
	}, true
}

// buildSessions returns recent injection sessions for the Activity view,
// optionally filtered by repo label and a since-cutoff (zero = no time limit).
// Repos is the full distinct repo list across the recent window — computed
// before filtering so the client's dropdown stays stable. All local and off the
// hook hot path: a scan of ≤500 in-memory rows, no extra query.
func (s *server) buildSessions(ctx context.Context, repoFilter string, since time.Time) injectionsPayload {
	rows, _ := s.store.RecentInjections(ctx, 500)
	// Map full card id → short id so each injected card can deep-link into the KB.
	metas, _ := s.store.AllCardsMeta(ctx)
	idToShort := make(map[string]string, len(metas))
	for _, m := range metas {
		idToShort[m.ID] = m.ShortID
	}
	// Distinct repos across the whole recent window (dropdown options), and the
	// row filter, in one pass. "all"/"" repoFilter means no repo constraint.
	repoSet := map[string]struct{}{}
	wantRepo := repoFilter != "" && repoFilter != "all"
	kept := make([]store.InjectionRow, 0, len(rows))
	for _, r := range rows {
		lbl := repoLabel(r.Cwd)
		if lbl != "" {
			repoSet[lbl] = struct{}{}
		}
		if wantRepo && lbl != repoFilter {
			continue
		}
		if !since.IsZero() {
			if t, ok := parseTS(r.TS); !ok || t.Before(since) {
				continue
			}
		}
		kept = append(kept, r)
	}
	repos := make([]string, 0, len(repoSet))
	for r := range repoSet {
		repos = append(repos, r)
	}
	sort.Strings(repos)

	var order []string
	groups := map[string][]store.InjectionRow{}
	for _, r := range kept {
		if _, ok := groups[r.SessionID]; !ok {
			order = append(order, r.SessionID)
		}
		groups[r.SessionID] = append(groups[r.SessionID], r)
	}
	out := []sessionPayload{}
	for i, sid := range order {
		if i >= 15 {
			break
		}
		rs := groups[sid]
		sort.SliceStable(rs, func(a, b int) bool { return rs[a].TS < rs[b].TS }) // chronological within session
		total := 0
		var evOrder []string
		evGroups := map[string][]store.InjectionRow{}
		for _, r := range rs {
			total += r.Tokens
			k := r.TS + "\x1f" + r.Event
			if _, ok := evGroups[k]; !ok {
				evOrder = append(evOrder, k)
			}
			evGroups[k] = append(evGroups[k], r)
		}
		var events []sessionEvent
		for _, k := range evOrder {
			g := evGroups[k]
			ev := normalizeEvent(g[0].Event)
			list := make([]injCard, 0, len(g))
			for _, r := range g {
				list = append(list, injCard{ID: r.CardID, Short: idToShort[r.CardID], Gran: r.Granularity, Tok: r.Tokens})
			}
			events = append(events, sessionEvent{
				At:    hhmm(g[0].TS),
				Ev:    ev,
				Cards: cardsSummary(ev, g),
				Gran:  joinGrans(g),
				Tok:   itoa(sumTokens(g)) + " tok",
				List:  list,
			})
		}
		cwd := sessionCwd(rs)
		out = append(out, sessionPayload{
			ID:       shortSession(sid),
			Repo:     repoLabel(cwd),
			RepoPath: cwd,
			Time:     humanizeTime(rs[len(rs)-1].TS),
			Tokens:   commas(total),
			Events:   events,
		})
	}
	return injectionsPayload{Repos: repos, Sessions: out}
}

func (s *server) buildRuns() []runPayload {
	led := llmtier.LoadLedger(config.StateDir(s.base))
	days := make([]string, 0, len(led.Days))
	for d := range led.Days {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	out := []runPayload{}
	for i, d := range days {
		if i >= 12 {
			break
		}
		// mined/clean/candidates/patterns are not persisted per-run (only the
		// spend ledger is); show the real spend and "—" for the rest rather
		// than invent counts.
		out = append(out, runPayload{
			Date: d, Mined: "—", Clean: "—", Candidates: "—", Patterns: "—",
			Spend: fmtUSD(led.Days[d].USD), Dot: "#57c785", Failed: false,
		})
	}
	return out
}

func (s *server) buildSettings() settingsPayload {
	c := s.config()
	led := llmtier.LoadLedger(config.StateDir(s.base))
	today := time.Now().UTC().Format("2006-01-02")
	groups := []setGroup{
		{Title: "Budgets", Items: []setItem{
			{Key: "push_budget", Desc: "max tokens injected per prompt", Value: itoa(c.PushBudget), Width: "90px"},
			{Key: "baseline_budget", Desc: "session-start baseline cap", Value: itoa(c.BaselineBudget), Width: "90px"},
		}},
		{Title: "Lifecycle", Items: []setItem{
			{Key: "candidate_ttl_days", Desc: "auto-expire unreviewed candidates", Value: itoa(c.Learn.CandidateTTLDays), Width: "70px"},
		}},
		{Title: "Caps & spend", Items: []setItem{
			{Key: "daily_usd_cap", Desc: "hard stop on learn spend", Value: fmt.Sprintf("%.2f", c.Learn.DailyUSDCap), Width: "80px"},
			{Key: "daily_call_cap", Desc: "model calls per day", Value: itoa(c.Learn.DailyCallCap), Width: "70px"},
			{Key: "max_jobs_per_run", Desc: "transcripts mined per run, newest first (−1 = no limit)", Value: itoa(c.Learn.MaxJobsPerRun), Width: "70px"},
		}},
		{Title: "Learning backend", Items: []setItem{
			{Key: "provider", Desc: "auto · anthropic · claude-cli · ollama · none", Value: c.Learn.Provider, Width: "120px"},
			{Key: "cheap_model", Desc: "routine mining model (e.g. qwen3 for ollama)", Value: c.Learn.CheapModel, Width: "180px"},
			{Key: "strong_model", Desc: "escalation model on schema failure", Value: c.Learn.StrongModel, Width: "180px"},
			{Key: "oauth_token_file", Desc: "file with CLAUDE_CODE_OAUTH_TOKEN (headless subscription auth)", Value: c.Learn.OAuthTokenFile, Width: "260px"},
			{Key: "anthropic_api_key_file", Desc: "file with ANTHROPIC_API_KEY (headless metered API)", Value: c.Learn.AnthropicAPIKeyFile, Width: "260px"},
		}},
		{Title: "Gate", Items: []setItem{
			{Key: "extra_acks", Desc: "always-skip acknowledgements", Value: strings.Join(c.ExtraAcks, ", "), Width: "160px"},
			{Key: "extra_stopwords", Desc: "never trigger on these", Value: strings.Join(c.ExtraStopwords, ", "), Width: "160px"},
			{Key: "repos", Desc: "watched repositories", Value: repoBasenames(c.Repos), Width: "160px"},
		}},
	}
	return settingsPayload{SpendToday: led.Days[today].USD, SpendCap: c.Learn.DailyUSDCap, Groups: groups}
}

// ---------- helpers ----------

func itoa(n int) string { return strconv.Itoa(n) }
func round(f float64) int {
	if f < 0 {
		return 0
	}
	return int(f + 0.5)
}
func fmtUSD(f float64) string { return fmt.Sprintf("$%.2f", f) }

func fmtPct(dumpTokens int, pct float64) string {
	if dumpTokens <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

func commas(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func humanizeTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK", float64(n)/1_000)
	default:
		return itoa(n)
	}
}

func mustDaily(v []int, _ error) []int { return v }

// normalizeTrend scales daily token totals to 0–100 bar heights with a small
// floor so non-zero days stay visible.
func normalizeTrend(daily []int) []int {
	if len(daily) == 0 {
		return []int{}
	}
	max := 0
	for _, v := range daily {
		if v > max {
			max = v
		}
	}
	out := make([]int, len(daily))
	for i, v := range daily {
		if max == 0 {
			out[i] = 6
			continue
		}
		out[i] = 6 + round(94*float64(v)/float64(max))
	}
	return out
}

var typeOrder = []struct {
	key    string
	letter string
}{{"rule", "R"}, {"style", "S"}, {"lesson", "L"}, {"pattern", "P"}, {"skill", "K"}, {"agent", "A"}}

func typeLettersSub(byType map[string]int) string {
	var parts []string
	for _, t := range typeOrder {
		if n := byType[t.key]; n > 0 {
			parts = append(parts, itoa(n)+t.letter)
		}
	}
	return strings.Join(parts, " · ")
}

func granFromBuckets(sv statsView) granPayload {
	var body, summary, hook int
	for _, b := range sv.Retrieval.Buckets {
		switch b.Granularity {
		case "body":
			body += b.Tokens
		case "summary":
			summary += b.Tokens
		case "hook":
			hook += b.Tokens
		}
	}
	total := body + summary + hook
	if total == 0 {
		return granPayload{}
	}
	pb := round(100 * float64(body) / float64(total))
	ps := round(100 * float64(summary) / float64(total))
	ph := 100 - pb - ps
	if ph < 0 {
		ph = 0
	}
	return granPayload{Body: pb, Summary: ps, Hook: ph}
}

// isStale mirrors cli.staleCards: a live (confirmed) card that was never
// injected in the retention window and shows no decayed pull activity. Kept in
// sync by hand — both encode the same "dormant card" definition.
func isStale(status string, injected bool, cs store.CardStats) bool {
	if status == "candidate" || status == "retired" {
		return false
	}
	if injected {
		return false
	}
	return cs.Expanded+cs.Referenced <= 0.1
}

func normStatus(s string) string {
	if s == "" {
		return "confirmed"
	}
	return s
}

func provenanceOf(c knowledge.Card) (source, model string) {
	source, model = "—", "—"
	if c.Provenance != nil {
		if c.Provenance.Source != "" {
			source = c.Provenance.Source
		}
		if c.Provenance.Model != "" {
			model = c.Provenance.Model
		}
	}
	return source, model
}

// extractEvidence pulls the first "**Evidence:**" line out of a mined card
// body (the miner appends it there) and returns the body with all evidence /
// reinforced annotation lines removed, so the console shows the prose body and
// the evidence quote separately.
func extractEvidence(body string) (evidence, clean string) {
	var keep []string
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if m, ok := stripMarker(t, "**Evidence:**"); ok {
			if evidence == "" {
				evidence = m
			}
			continue
		}
		if _, ok := stripMarker(t, "**Reinforced:**"); ok {
			continue
		}
		keep = append(keep, ln)
	}
	return evidence, strings.TrimSpace(strings.Join(keep, "\n"))
}
func stripMarker(line, marker string) (string, bool) {
	if strings.HasPrefix(line, marker) {
		return strings.TrimSpace(strings.TrimPrefix(line, marker)), true
	}
	return "", false
}

// fileTimes returns the card file's mtime as a created date and a humanized
// "ago" label. mtime is culi's clock for card freshness (see the janitor).
func fileTimes(kdir, relPath string) (created, ago string) {
	info, err := os.Stat(filepath.Join(kdir, filepath.FromSlash(relPath)))
	if err != nil {
		return "—", ""
	}
	m := info.ModTime()
	return m.Format("2006-01-02 15:04"), humanizeAgo(m)
}

func humanizeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	default:
		return itoa(int(d.Hours()/24)) + "d ago"
	}
}

// parseTS parses the injection-log timestamp, tolerating the SQLite default
// datetime format and RFC3339.
func parseTS(ts string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func hhmm(ts string) string {
	if t, ok := parseTS(ts); ok {
		return t.Format("15:04")
	}
	return ts
}

func humanizeTime(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ts
	}
	now := time.Now()
	day := t.Format("15:04")
	switch {
	case sameDay(t, now):
		return "today " + day
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "yesterday " + day
	default:
		return t.Format("Jan 2 15:04")
	}
}
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func normalizeEvent(ev string) string {
	switch ev {
	case "user-prompt-submit":
		return "user-prompt"
	case "session-start":
		return "session-start"
	default:
		return ev
	}
}

func cardsSummary(ev string, rs []store.InjectionRow) string {
	if ev == "session-start" {
		return itoa(len(rs)) + " baseline cards"
	}
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.CardID)
	}
	if len(ids) <= 2 {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:2], ", ") + " +" + itoa(len(ids)-2) + " more"
}

func joinGrans(rs []store.InjectionRow) string {
	seen := map[string]bool{}
	var order []string
	for _, r := range rs {
		if !seen[r.Granularity] {
			seen[r.Granularity] = true
			order = append(order, r.Granularity)
		}
	}
	sort.Strings(order)
	return strings.Join(order, "+")
}

func sumTokens(rs []store.InjectionRow) int {
	n := 0
	for _, r := range rs {
		n += r.Tokens
	}
	return n
}

func shortSession(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// sessionCwd returns the working directory a session ran in — the most recent
// non-empty cwd across its injection rows (rows are chronological, so scanning
// from the end finds the latest). "" when no row carries a cwd (pre-v2 logs).
func sessionCwd(rs []store.InjectionRow) string {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Cwd != "" {
			return rs[i].Cwd
		}
	}
	return ""
}

// repoLabel is the repo name shown in the Activity session header — the
// basename of the cwd. "" passes through so the console renders its
// "conversation" fallback.
func repoLabel(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}

func repoBasenames(repos []string) string {
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, filepath.Base(r))
	}
	return strings.Join(names, ", ")
}
