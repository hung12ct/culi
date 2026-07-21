package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// --- search_context ---

type searchIn struct {
	Query string `json:"query" jsonschema:"what to look for — natural language or keywords"`
	Dir   string `json:"dir,omitempty" jsonschema:"directory to resolve repo/language scope from (default: server working directory)"`
}

type searchHit struct {
	ID      string  `json:"id"`
	ShortID string  `json:"short_id"`
	Type    string  `json:"type"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
	Score   float64 `json:"score"`
}

type searchOut struct {
	Results []searchHit `json:"results"`
	Note    string      `json:"note,omitempty"`
}

// searchContext is the pull-side twin of the hook funnel: same hybrid
// retrieval, no gate (an explicit tool call is never an "ack" to skip).
func (s *Server) searchContext(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, searchOut{}, fmt.Errorf("query must not be empty")
	}
	dir := in.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	r := &retrieve.Retriever{Store: s.store, Embedder: s.emb, Model: s.cfg.Ollama.Model}
	cands, err := r.Retrieve(ctx, in.Query, retrieve.DetectScope(dir))
	if err != nil {
		return nil, searchOut{}, err
	}
	out := searchOut{Results: make([]searchHit, 0, len(cands))}
	for _, c := range cands {
		out.Results = append(out.Results, searchHit{
			ID: c.Card.ID, ShortID: c.Card.ShortID, Type: c.Card.Type,
			Title: c.Card.Title, Summary: c.Card.Summary, Score: c.Score,
		})
	}
	if len(out.Results) == 0 {
		out.Note = "no cards matched — the store may have nothing for this topic yet"
	}
	return nil, out, nil
}

// --- expand_card ---

type expandIn struct {
	ID string `json:"id" jsonschema:"card ID (e.g. rules/go-error-wrapping) or 4-hex short ID from a ▸ pointer"`
}

type expandOut struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Attachments []string `json:"attachments,omitempty"` // absolute paths, readable via the Read tool
}

func (s *Server) expandCard(ctx context.Context, _ *mcp.CallToolRequest, in expandIn) (*mcp.CallToolResult, expandOut, error) {
	c, err := s.store.CardByID(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, expandOut{}, err
	}
	out := expandOut{ID: c.ID, Type: c.Type, Title: c.Title, Body: c.Body}
	// Skill attachments live next to SKILL.md; hand back paths, not contents —
	// Claude reads what it actually needs.
	if dir := filepath.Dir(c.Path); strings.HasPrefix(c.Path, "skills/") {
		abs := filepath.Join(config.KnowledgeDir(s.base), filepath.FromSlash(dir))
		entries, err := os.ReadDir(abs)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || e.Name() == "SKILL.md" {
					continue
				}
				out.Attachments = append(out.Attachments, filepath.Join(abs, e.Name()))
			}
		}
	}
	// An expansion is the strongest organic relevance signal (+3 weight).
	// Best-effort: a stats hiccup must never fail a successful expand.
	_ = s.store.AddFeedback(ctx, c.ID, store.FeedbackExpanded, 1)
	return nil, out, nil
}

// --- save_lesson ---

type saveIn struct {
	Title    string `json:"title" jsonschema:"short imperative title for the lesson"`
	Markdown string `json:"markdown" jsonschema:"the lesson body in markdown"`
	Summary  string `json:"summary,omitempty" jsonschema:"one-line summary (~60 tokens); defaults to the first line of markdown"`
	Scope    string `json:"scope,omitempty" jsonschema:"global, lang:<x>, or repo:<name>; defaults to global"`
}

type saveOut struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Merged bool   `json:"merged,omitempty"` // true ⇒ folded into an existing lesson, not created
	Note   string `json:"note,omitempty"`   // human-readable outcome (what happened to the card)
}

func (s *Server) saveLesson(ctx context.Context, _ *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, saveOut, error) {
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Markdown)
	if title == "" || body == "" {
		return nil, saveOut{}, fmt.Errorf("title and markdown must not be empty")
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "global"
	}
	if scope != "global" && !strings.HasPrefix(scope, "lang:") && !strings.HasPrefix(scope, "repo:") {
		return nil, saveOut{}, fmt.Errorf("scope must be global, lang:<x>, or repo:<name>; got %q", scope)
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		summary = firstLine(body)
	}

	// Smart dedup: if culi already holds a semantically-close lesson it
	// authored, fold the new knowledge in rather than creating a duplicate.
	// Append-only, so it never destroys the existing card (C4).
	if out, ok := s.mergeIntoExisting(ctx, title, summary, body, scope); ok {
		return nil, out, nil
	}

	// save_lesson is explicit user intent ⇒ instant confirm (plan §learning
	// lifecycle) — retrievable next prompt, no review gate.
	card := knowledge.Card{
		Type: "lesson", Title: title, Summary: summary, Body: body,
		Scopes: []string{scope}, Status: "confirmed",
		Provenance: &knowledge.Provenance{Source: "mcp"},
	}
	rendered, err := knowledge.Render(card)
	if err != nil {
		return nil, saveOut{}, err
	}

	kdir := config.KnowledgeDir(s.base)
	year := time.Now().UTC().Format("2006")
	rel := filepath.Join("lessons", year, slugify(title)+".md")
	abs := filepath.Join(kdir, rel)
	if _, err := os.Stat(abs); err == nil {
		// Never destroy user content (C4): disambiguate instead of overwrite.
		rel = filepath.Join("lessons", year, slugify(title)+"-"+knowledge.ShortID(title+body)+".md")
		abs = filepath.Join(kdir, rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, saveOut{}, fmt.Errorf("creating lessons dir: %w", err)
	}
	if err := os.WriteFile(abs, rendered, 0o644); err != nil {
		return nil, saveOut{}, fmt.Errorf("writing lesson: %w", err)
	}

	if _, err := indexer.Sync(ctx, s.store, kdir); err != nil {
		return nil, saveOut{}, err
	}
	// Vector for the new card: best-effort, bounded — BM25 finds it either way.
	ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = indexer.EmbedMissing(ectx, s.store, s.emb, s.cfg.Ollama.Model)

	id := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	_ = knowledge.Commit(kdir, "mcp: save_lesson "+id) // governance trail, best-effort
	return nil, saveOut{ID: id, Path: abs, Note: "created new lesson " + id}, nil
}

// Merge thresholds: cosine at/above which an explicit save_lesson updates an
// existing culi-authored lesson instead of creating a new card, and the lexical
// Jaccard fallback when Ollama is down. Both sit just below the miner's dup
// gates — explicit user intent tolerates a slightly looser match.
const (
	saveMergeSim = 0.90 // embed cosine
	saveMergeJac = 0.60 // lexical fallback (matches the miner's jacDup)
)

// mergeIntoExisting folds a save_lesson into a semantically-close lesson culi
// already authored (the "recognize it, don't duplicate" path). It is
// append-only — existing prose is never rewritten, only extended under a dated
// marker — so C4 holds. ok=false (no close match, or a hand-authored match that
// must never round-trip through Render) tells the caller to create a fresh card
// as before.
func (s *Server) mergeIntoExisting(ctx context.Context, title, summary, body, scope string) (saveOut, bool) {
	metas, err := s.store.AllCardsMeta(ctx)
	if err != nil {
		return saveOut{}, false
	}
	// Candidate targets: live lessons in the same scope. Never merge across
	// scopes or into a retired card (which must not swallow its successor).
	lessons := make([]store.StoredCard, 0)
	for _, sc := range metas {
		if sc.Type == "lesson" && sc.Status != "retired" && slices.Contains(sc.Scopes, scope) {
			lessons = append(lessons, sc)
		}
	}
	if len(lessons) == 0 {
		return saveOut{}, false
	}
	best, sim, ok := s.closestLesson(ctx, title, summary, lessons)
	if !ok {
		return saveOut{}, false
	}

	kdir := config.KnowledgeDir(s.base)
	fc, err := knowledge.ReadCard(kdir, best.Path)
	if err != nil || !culiAuthored(fc) {
		return saveOut{}, false // hand-authored → never rewrite (C4); create new instead
	}
	abs := filepath.Join(kdir, filepath.FromSlash(best.Path))
	// Already captured verbatim: point at the existing card, change nothing.
	if strings.Contains(fc.Body, strings.TrimSpace(body)) {
		return saveOut{ID: best.ID, Path: abs, Merged: true, Note: "already captured in lesson " + best.ID}, true
	}

	marker := "\n\n**Update " + time.Now().UTC().Format("2006-01-02") + ":** " + strings.TrimSpace(body)
	if err := knowledge.UpdateFile(kdir, best.Path, func(c *knowledge.Card) {
		c.Body = strings.TrimRight(c.Body, "\n") + marker
		c.Observations++ // reinforced by an explicit save
	}); err != nil {
		return saveOut{}, false
	}
	if _, err := indexer.Sync(ctx, s.store, kdir); err != nil {
		return saveOut{}, false
	}
	ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = indexer.EmbedMissing(ectx, s.store, s.emb, s.cfg.Ollama.Model) // best-effort re-embed
	_ = knowledge.Commit(kdir, "mcp: save_lesson merge into "+best.ID)
	return saveOut{ID: best.ID, Path: abs, Merged: true,
		Note: fmt.Sprintf("merged into existing lesson %s (similarity %.2f) — no duplicate created", best.ID, sim)}, true
}

// closestLesson returns the lesson most similar to the new one, embeddings-first
// (cosine ≥ saveMergeSim) with a lexical Jaccard fallback (≥ saveMergeJac) when
// Ollama is down — the same primary/fallback pairing the miner's dedup uses.
// When vectors ARE available it trusts their verdict and never falls through to
// lexical (which would risk a looser wrong-merge).
func (s *Server) closestLesson(ctx context.Context, title, summary string, lessons []store.StoredCard) (store.StoredCard, float64, bool) {
	if s.emb != nil {
		if vecs, err := s.store.Embeddings(ctx, s.cfg.Ollama.Model); err == nil && len(vecs) > 0 {
			ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
			qv, err := s.emb.Embed(ectx, []string{title + "\n" + summary})
			cancel()
			if err == nil && len(qv) == 1 {
				best, bestSim, found := store.StoredCard{}, saveMergeSim, false
				for _, sc := range lessons {
					if v, ok := vecs[sc.Rowid]; ok {
						if sim := embed.Dot(qv[0], v); sim >= bestSim {
							bestSim, best, found = sim, sc, true
						}
					}
				}
				return best, bestSim, found
			}
		}
	}
	// Lexical fallback: term-set Jaccard over title+summary.
	cand := retrieve.Terms(title+" "+summary, 0)
	best, bestSim, found := store.StoredCard{}, saveMergeJac, false
	for _, sc := range lessons {
		if sim := retrieve.Jaccard(cand, retrieve.Terms(sc.Title+" "+sc.Summary, 0)); sim >= bestSim {
			bestSim, best, found = sim, sc, true
		}
	}
	return best, bestSim, found
}

// culiAuthored reports whether a card was written by culi (import, learning, or
// an earlier save_lesson) and may therefore be rewritten via UpdateFile.
// Hand-authored files never round-trip through Render (C4).
func culiAuthored(c knowledge.Card) bool {
	if c.Provenance == nil {
		return false
	}
	switch c.Provenance.Source {
	case "learn", "mcp", "import":
		return true
	}
	return false
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if slug == "" {
		return "lesson"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimLeft(s, "# "))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
