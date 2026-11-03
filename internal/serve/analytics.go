package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/store"
)

const noRepoKey = "__no_repository__"

type analyticsFilter struct {
	Harness string
	Repo    string
}

type analyticsRepo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

type analyticsSummary struct {
	ActiveCards       int `json:"activeCards"`
	Sessions          int `json:"sessions"`
	Deliveries        int `json:"deliveries"`
	Tokens            int `json:"tokens"`
	CrossHarnessCards int `json:"crossHarnessCards"`
	Helpful           int `json:"helpful"`
	Noisy             int `json:"noisy"`
	Expensive         int `json:"expensive"`
	Uncertain         int `json:"uncertain"`
}

type analyticsHarness struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	Sessions   int    `json:"sessions"`
	Deliveries int    `json:"deliveries"`
	Tokens     int    `json:"tokens"`
}

type analyticsCard struct {
	ID          string             `json:"id"`
	Short       string             `json:"short"`
	Title       string             `json:"title"`
	Type        string             `json:"type"`
	Status      string             `json:"status"`
	ScopeLabels []string           `json:"scopeLabels"`
	Available   bool               `json:"available"`
	Sessions    int                `json:"sessions"`
	Deliveries  int                `json:"deliveries"`
	Tokens      int                `json:"tokens"`
	LastUsed    string             `json:"lastUsed"`
	Harnesses   []analyticsHarness `json:"harnesses"`
	Repos       []string           `json:"repos"`
	Bucket      string             `json:"bucket"`   // effectiveness verdict (store.Eff*)
	PullRate    string             `json:"pullRate"` // pulls per injection, formatted ("" when no injections)

	lastUsedRaw string
}

type analyticsPayload struct {
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"`
	Range     string           `json:"range"`
	Summary   analyticsSummary `json:"summary"`
	Repos     []analyticsRepo  `json:"repos"`
	Harnesses []harnessOpt     `json:"harnesses"`
	Cards     []analyticsCard  `json:"cards"`
}

type repoIdentity struct {
	Key   string
	Label string
	Path  string
}

type analyticsAccumulator struct {
	sessions   map[string]struct{}
	deliveries int
	tokens     int
	lastUsed   string
	harnesses  map[string]*analyticsSlice
	repos      map[string]struct{}
}

type analyticsSlice struct {
	sessions   map[string]struct{}
	deliveries int
	tokens     int
}

// resolveRepo maps a recorded working directory to its containing Git root.
// It runs only while building console analytics, never during injection.
func resolveRepo(cwd string) repoIdentity {
	if cwd == "" {
		return repoIdentity{Key: noRepoKey, Label: "No repository"}
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return repoIdentity{Key: noRepoKey, Label: "No repository"}
	}
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return repoIdentity{Key: dir, Label: filepath.Base(dir), Path: dir}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return repoIdentity{Key: noRepoKey, Label: "No repository"}
}

func (s *server) buildAnalytics(ctx context.Context, filter analyticsFilter) (analyticsPayload, error) {
	rows, err := s.store.InjectionsSince(ctx, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		return analyticsPayload{}, err
	}
	meta, err := s.store.AllCardsMeta(ctx)
	if err != nil {
		return analyticsPayload{}, err
	}
	metaByID := make(map[string]store.StoredCard, len(meta))
	for _, card := range meta {
		metaByID[card.ID] = card
	}

	// Effectiveness inputs are deliberately unfiltered (lifetime decayed
	// counters + full-window usage): a card's verdict must not flip when the
	// harness/repo filter changes. Both best-effort — a missing map classifies
	// everything from zero values ("uncertain") instead of failing the view.
	cardStats, _ := s.store.AllCardStats(ctx, time.Now().UTC())
	windowUsage, _ := s.store.CardWindowUsage(ctx)

	opts := indexAnalyticsRows(rows)
	byCard, sessions := accumulateAnalytics(rows, opts.repoByRow, filter)

	payload := analyticsPayload{
		Available: true,
		Range:     "Last 7 days",
		Harnesses: harnessOptions(opts.harnesses),
		Repos:     sortedRepos(opts.repos),
	}
	payload.Summary.Sessions = sessions
	for id, a := range byCard {
		eff := store.ClassifyEffectiveness(cardStats[id], windowUsage[id])
		card := analyticsCardFor(id, a, eff, payload.Harnesses)
		if m, ok := metaByID[id]; ok {
			card.withMeta(m)
		}
		payload.Summary.tally(card)
		payload.Cards = append(payload.Cards, card)
	}
	payload.Summary.ActiveCards = len(payload.Cards)
	sortAnalyticsCards(payload.Cards)
	return payload, nil
}

// analyticsOptions describes the complete window, not only the filtered
// result, so changing one filter never makes the other options disappear.
type analyticsOptions struct {
	repoByRow []repoIdentity // resolved repo for each row, by index
	repos     map[string]analyticsRepo
	harnesses map[string]struct{}
}

func indexAnalyticsRows(rows []store.InjectionRow) analyticsOptions {
	opts := analyticsOptions{
		repoByRow: make([]repoIdentity, len(rows)),
		repos:     map[string]analyticsRepo{},
		harnesses: map[string]struct{}{harness.Claude.String(): {}, harness.Codex.String(): {}},
	}
	cache := map[string]repoIdentity{}
	for i, row := range rows {
		repo, ok := cache[row.Cwd]
		if !ok {
			repo = resolveRepo(row.Cwd)
			cache[row.Cwd] = repo
		}
		opts.repoByRow[i] = repo
		opts.repos[repo.Key] = analyticsRepo(repo)
		if row.Harness != "" {
			opts.harnesses[row.Harness] = struct{}{}
		}
	}
	return opts
}

// accumulateAnalytics folds the filtered rows per card and counts the
// distinct sessions they came from.
func accumulateAnalytics(rows []store.InjectionRow, repoByRow []repoIdentity, filter analyticsFilter) (map[string]*analyticsAccumulator, int) {
	byCard := map[string]*analyticsAccumulator{}
	sessions := map[string]struct{}{}
	for i, row := range rows {
		repo := repoByRow[i]
		if filter.Harness != "" && filter.Harness != "all" && row.Harness != filter.Harness {
			continue
		}
		if filter.Repo != "" && filter.Repo != "all" && repo.Key != filter.Repo {
			continue
		}
		a := byCard[row.CardID]
		if a == nil {
			a = &analyticsAccumulator{
				sessions:  map[string]struct{}{},
				harnesses: map[string]*analyticsSlice{},
				repos:     map[string]struct{}{},
			}
			byCard[row.CardID] = a
		}
		a.add(row, repo)
		sessions[row.SessionID] = struct{}{}
	}
	return byCard, len(sessions)
}

func (a *analyticsAccumulator) add(row store.InjectionRow, repo repoIdentity) {
	a.sessions[row.SessionID] = struct{}{}
	a.deliveries++
	a.tokens += row.Tokens
	if row.TS > a.lastUsed {
		a.lastUsed = row.TS
	}
	a.repos[repo.Label] = struct{}{}
	h := a.harnesses[row.Harness]
	if h == nil {
		h = &analyticsSlice{sessions: map[string]struct{}{}}
		a.harnesses[row.Harness] = h
	}
	h.sessions[row.SessionID] = struct{}{}
	h.deliveries++
	h.tokens += row.Tokens
}

// sortedRepos lists the window's repos by label. Empty yields nil, which the
// console already renders as "no repos".
func sortedRepos(byKey map[string]analyticsRepo) []analyticsRepo {
	var repos []analyticsRepo
	for _, repo := range byKey {
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Label != repos[j].Label {
			return repos[i].Label < repos[j].Label
		}
		return repos[i].Key < repos[j].Key
	})
	return repos
}

// analyticsCardFor builds one card row from its accumulator. Without card
// metadata (a card deleted since it was injected) it stays "unavailable".
func analyticsCardFor(id string, a *analyticsAccumulator, eff store.Effectiveness, options []harnessOpt) analyticsCard {
	card := analyticsCard{
		ID: id, Title: id, Type: "unknown", Status: "unavailable",
		Sessions: len(a.sessions), Deliveries: a.deliveries, Tokens: a.tokens,
		LastUsed: humanizeTime(a.lastUsed), lastUsedRaw: a.lastUsed, Bucket: eff.Bucket,
	}
	if eff.PullRate > 0 {
		card.PullRate = fmt.Sprintf("%.0f%%", 100*eff.PullRate)
	}
	for code, slice := range a.harnesses {
		label := code
		for _, option := range options {
			if option.Code == code {
				label = option.Label
				break
			}
		}
		card.Harnesses = append(card.Harnesses, analyticsHarness{
			Code: code, Label: label, Sessions: len(slice.sessions),
			Deliveries: slice.deliveries, Tokens: slice.tokens,
		})
	}
	sort.Slice(card.Harnesses, func(i, j int) bool { return card.Harnesses[i].Code < card.Harnesses[j].Code })
	for repo := range a.repos {
		card.Repos = append(card.Repos, repo)
	}
	sort.Strings(card.Repos)
	return card
}

func (c *analyticsCard) withMeta(m store.StoredCard) {
	c.Short = m.ShortID
	c.Title = m.Title
	c.Type = m.Type
	c.Status = m.Status
	c.ScopeLabels = append([]string(nil), m.Scopes...)
	c.Available = true
}

// tally adds one card to the window totals and its effectiveness bucket.
func (sum *analyticsSummary) tally(card analyticsCard) {
	switch card.Bucket {
	case store.EffHelpful:
		sum.Helpful++
	case store.EffNoisy:
		sum.Noisy++
	case store.EffExpensive:
		sum.Expensive++
	default:
		sum.Uncertain++
	}
	sum.Deliveries += card.Deliveries
	sum.Tokens += card.Tokens
	if len(card.Harnesses) > 1 {
		sum.CrossHarnessCards++
	}
}

// sortAnalyticsCards orders by sessions, then deliveries, then recency, then
// title, so the most widely useful cards lead.
func sortAnalyticsCards(cards []analyticsCard) {
	sort.Slice(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if a.Sessions != b.Sessions {
			return a.Sessions > b.Sessions
		}
		if a.Deliveries != b.Deliveries {
			return a.Deliveries > b.Deliveries
		}
		if a.lastUsedRaw != b.lastUsedRaw {
			return a.lastUsedRaw > b.lastUsedRaw
		}
		return a.Title < b.Title
	})
}
