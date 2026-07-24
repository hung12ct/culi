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

	// Options describe the complete window, not only the currently filtered
	// result, so changing one filter never makes the other options disappear.
	reposByKey := map[string]analyticsRepo{}
	harnessSet := map[string]struct{}{
		harness.Claude.String(): {},
		harness.Codex.String():  {},
	}
	repoCache := map[string]repoIdentity{}
	repoByRow := make([]repoIdentity, len(rows))
	for i, row := range rows {
		repo, ok := repoCache[row.Cwd]
		if !ok {
			repo = resolveRepo(row.Cwd)
			repoCache[row.Cwd] = repo
		}
		repoByRow[i] = repo
		reposByKey[repo.Key] = analyticsRepo(repo)
		if row.Harness != "" {
			harnessSet[row.Harness] = struct{}{}
		}
	}

	byCard := map[string]*analyticsAccumulator{}
	summarySessions := map[string]struct{}{}
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
		summarySessions[row.SessionID] = struct{}{}
	}

	payload := analyticsPayload{
		Available: true,
		Range:     "Last 7 days",
		Harnesses: harnessOptions(harnessSet),
	}
	for _, repo := range reposByKey {
		payload.Repos = append(payload.Repos, repo)
	}
	sort.Slice(payload.Repos, func(i, j int) bool {
		if payload.Repos[i].Label != payload.Repos[j].Label {
			return payload.Repos[i].Label < payload.Repos[j].Label
		}
		return payload.Repos[i].Key < payload.Repos[j].Key
	})

	payload.Summary.Sessions = len(summarySessions)
	for id, a := range byCard {
		card := analyticsCard{
			ID:          id,
			Title:       id,
			Type:        "unknown",
			Status:      "unavailable",
			Available:   false,
			Sessions:    len(a.sessions),
			Deliveries:  a.deliveries,
			Tokens:      a.tokens,
			LastUsed:    humanizeTime(a.lastUsed),
			lastUsedRaw: a.lastUsed,
		}
		eff := store.ClassifyEffectiveness(cardStats[id], windowUsage[id])
		card.Bucket = eff.Bucket
		if eff.PullRate > 0 {
			card.PullRate = fmt.Sprintf("%.0f%%", 100*eff.PullRate)
		}
		switch eff.Bucket {
		case store.EffHelpful:
			payload.Summary.Helpful++
		case store.EffNoisy:
			payload.Summary.Noisy++
		case store.EffExpensive:
			payload.Summary.Expensive++
		default:
			payload.Summary.Uncertain++
		}
		if m, ok := metaByID[id]; ok {
			card.Short = m.ShortID
			card.Title = m.Title
			card.Type = m.Type
			card.Status = m.Status
			card.ScopeLabels = append([]string(nil), m.Scopes...)
			card.Available = true
		}
		for code, slice := range a.harnesses {
			label := code
			for _, option := range payload.Harnesses {
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
		payload.Cards = append(payload.Cards, card)
		payload.Summary.Deliveries += card.Deliveries
		payload.Summary.Tokens += card.Tokens
		if len(card.Harnesses) > 1 {
			payload.Summary.CrossHarnessCards++
		}
	}
	payload.Summary.ActiveCards = len(payload.Cards)
	sort.Slice(payload.Cards, func(i, j int) bool {
		a, b := payload.Cards[i], payload.Cards[j]
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
	return payload, nil
}
