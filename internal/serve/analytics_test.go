package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

func TestBuildAnalyticsRanksAndFilters(t *testing.T) {
	ctx := context.Background()
	s := openAnalyticsStore(t)
	repoA := filepath.Join(t.TempDir(), "repo-a")
	repoB := filepath.Join(t.TempDir(), "repo-b")
	for _, dir := range []string{filepath.Join(repoA, ".git"), filepath.Join(repoA, "service"), filepath.Join(repoB, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	upsertAnalyticsCard(t, s, "rules/a", "Card A")
	upsertAnalyticsCard(t, s, "rules/b", "Card B")
	record := func(session, card, cwd string, h harness.Harness, tokens int) {
		t.Helper()
		if err := s.RecordInjections(ctx, session, "user-prompt-submit", "", cwd, h, []store.InjectionRecord{
			{CardID: card, Granularity: store.GranSummary, Tokens: tokens},
		}); err != nil {
			t.Fatal(err)
		}
	}
	record("claude:s1", "rules/a", filepath.Join(repoA, "service"), harness.Claude, 40)
	record("claude:s1", "rules/a", filepath.Join(repoA, "service"), harness.Claude, 20)
	record("codex:s2", "rules/a", repoA, harness.Codex, 30)
	record("codex:s3", "rules/b", repoB, harness.Codex, 25)
	record("codex:s3", "rules/gone", repoB, harness.Codex, 10)

	srv := &server{store: s}
	got, err := srv.buildAnalytics(ctx, analyticsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Summary.Sessions != 3 || got.Summary.Deliveries != 5 || got.Summary.Tokens != 125 {
		t.Fatalf("summary = %+v, available %v", got.Summary, got.Available)
	}
	if len(got.Cards) != 3 || got.Cards[0].ID != "rules/a" || got.Cards[0].Sessions != 2 || got.Cards[0].Deliveries != 3 {
		t.Fatalf("cards = %+v", got.Cards)
	}
	if len(got.Cards[0].Harnesses) != 2 || got.Summary.CrossHarnessCards != 1 {
		t.Fatalf("cross-harness aggregation = %+v / %+v", got.Cards[0].Harnesses, got.Summary)
	}
	var removed *analyticsCard
	for i := range got.Cards {
		if got.Cards[i].ID == "rules/gone" {
			removed = &got.Cards[i]
			break
		}
	}
	if removed == nil || removed.Available || removed.Title != "rules/gone" {
		t.Fatalf("removed card = %+v", removed)
	}
	if len(got.Repos) != 2 || got.Repos[0].Path != repoA || got.Repos[0].Label != "repo-a" {
		t.Fatalf("repos = %+v", got.Repos)
	}

	// Effectiveness: rules/a has pulls (helpful), the others have no signal
	// and few observations (uncertain). Verdicts must survive filtering.
	if err := s.AddFeedback(ctx, "rules/a", store.FeedbackInjected, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFeedback(ctx, "rules/a", store.FeedbackExpanded, 2); err != nil {
		t.Fatal(err)
	}
	got, err = srv.buildAnalytics(ctx, analyticsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Cards[0].Bucket != store.EffHelpful || got.Cards[0].PullRate != "40%" {
		t.Fatalf("card verdict = %q / %q", got.Cards[0].Bucket, got.Cards[0].PullRate)
	}
	if got.Summary.Helpful != 1 || got.Summary.Uncertain != 2 {
		t.Fatalf("verdict summary = %+v", got.Summary)
	}
	filtered, err := srv.buildAnalytics(ctx, analyticsFilter{Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range filtered.Cards {
		if c.ID == "rules/a" && c.Bucket != store.EffHelpful {
			t.Fatalf("verdict flipped under filter: %+v", c)
		}
	}

	codex, err := srv.buildAnalytics(ctx, analyticsFilter{Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if codex.Summary.Sessions != 2 || codex.Cards[0].Sessions != 1 {
		t.Fatalf("codex filter = %+v / %+v", codex.Summary, codex.Cards)
	}
	repoOnly, err := srv.buildAnalytics(ctx, analyticsFilter{Repo: repoA})
	if err != nil {
		t.Fatal(err)
	}
	if repoOnly.Summary.Sessions != 2 || len(repoOnly.Cards) != 1 || repoOnly.Cards[0].ID != "rules/a" {
		t.Fatalf("repo filter = %+v / %+v", repoOnly.Summary, repoOnly.Cards)
	}
}

func TestRepoIdentityFallsBackOutsideGit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain-folder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := resolveRepo(dir)
	if got.Key != noRepoKey || got.Label != "No repository" || got.Path != "" {
		t.Fatalf("resolveRepo = %+v", got)
	}
}

func TestAnalyticsHandlerReportsUnavailable(t *testing.T) {
	s := openAnalyticsStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv := &server{store: s}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics", nil)
	srv.handleAnalytics(rr, req)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"available":false`) {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func openAnalyticsStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func upsertAnalyticsCard(t *testing.T, s *store.Store, id, title string) {
	t.Helper()
	c := knowledge.Card{
		ID: id, Path: id + ".md", Type: "rule", Title: title, Summary: "summary", Body: "body",
		Scopes: []string{"global"}, TokSummary: 2, TokBody: 2, ContentHash: "hash-" + id,
	}
	if err := s.UpsertCard(context.Background(), c, 1, 10); err != nil {
		t.Fatal(err)
	}
}
