package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hung12ct/culi/internal/config"
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
	ID   string `json:"id"`
	Path string `json:"path"`
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
	return nil, saveOut{ID: id, Path: abs}, nil
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
