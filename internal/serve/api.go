package serve

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/mine"
	"github.com/hung12ct/culi/internal/store"
)

// writeJSON emits v as JSON. Errors here are unrecoverable (client hung up);
// there is nothing useful to do but drop the response.
func (s *server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// guardLocal rejects state-changing requests that don't originate from a
// local, same-origin caller. The console binds to localhost, but a browser can
// still be steered into it two ways: DNS rebinding (a remote page rebinds its
// host to 127.0.0.1, so the Host header is the attacker's domain) and plain
// cross-origin POSTs (the Origin header is the attacker's domain). Requiring a
// localhost Host and, when present, a localhost Origin closes both — cheap
// hardening for a tool whose writes flip card status in the operator's store.
func (s *server) guardLocal(w http.ResponseWriter, r *http.Request) bool {
	if !localHostname(r.Host) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "non-local host rejected"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !localHostname(u.Host) {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return false
		}
	}
	return true
}

// localHostname reports whether host (with or without a :port) is a loopback name.
func localHostname(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	default:
		return false
	}
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildStatus(r.Context()))
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildOverview(r.Context()))
}

func (s *server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildCandidates(r.Context()))
}

func (s *server) handleCards(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildCards(r.Context()))
}

func (s *server) handleCard(w http.ResponseWriter, r *http.Request) {
	v, ok := s.buildCardDetail(r.Context(), r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, v)
}

func (s *server) handleInjections(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildSessions(r.Context()))
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildRuns())
}

func (s *server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildSettings())
}

// handleApprove confirms a candidate (candidate→confirmed, retiring any
// superseded card) exactly as `culi review --approve` does, then re-syncs the
// index and commits to the knowledge git history.
func (s *server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := r.Context()
	sc, err := s.store.CardByID(ctx, r.PathValue("id"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
		return
	}
	retired, err := mine.Confirm(ctx, s.store, s.kdir, sc.ID)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := knowledge.Commit(s.kdir, "review: approved "+sc.ID); err != nil {
		log.Printf("serve: commit after approve %s: %v", sc.ID, err)
	}
	resp := map[string]any{"ok": true}
	if retired != "" {
		resp["retired"] = knowledge.ShortID(retired)
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// handleReject retires a candidate (kept on disk, unindexed) like
// `culi review --reject`.
func (s *server) handleReject(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := r.Context()
	sc, err := s.store.CardByID(ctx, r.PathValue("id"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
		return
	}
	if err := mine.Reject(ctx, s.store, s.kdir, sc.ID); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := knowledge.Commit(s.kdir, "review: rejected "+sc.ID); err != nil {
		log.Printf("serve: commit after reject %s: %v", sc.ID, err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCardAction applies a non-candidate card action from the KB / Overview
// screens: "down" (downvote — reversible ranking signal), "retire" (status
// flip, reversible; culi-authored cards only), or "remove" (delete the file —
// git-revertible, mirrors `culi card rm`). The card ID travels in the JSON
// body because full IDs contain "/", which a path segment can't carry cleanly.
func (s *server) handleCardAction(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := r.Context()
	sc, err := s.store.CardByID(ctx, req.ID)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
		return
	}

	switch req.Action {
	case "down":
		if err := s.store.AddFeedback(ctx, sc.ID, store.FeedbackDownvoted, 1); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "retire":
		ok, err := mine.RetireCard(ctx, s.store, s.kdir, sc.ID)
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !ok {
			// Hand-authored (non-culi) cards never round-trip through Render (C4);
			// say so instead of silently doing nothing.
			s.writeJSON(w, http.StatusOK, map[string]any{
				"ok":   false,
				"note": "hand-authored card — edit its file to retire, or use Remove",
			})
			return
		}
		if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := knowledge.Commit(s.kdir, "card: retired "+sc.ID); err != nil {
			log.Printf("serve: commit after retire %s: %v", sc.ID, err)
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "remove":
		full := filepath.Join(s.kdir, filepath.FromSlash(sc.Path))
		if err := os.Remove(full); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := knowledge.Commit(s.kdir, "card: removed "+sc.ID); err != nil {
			log.Printf("serve: commit after remove %s: %v", sc.ID, err)
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
	}
}

// culiEditable reports whether a card was machine-authored by culi and is
// therefore safe to rewrite through knowledge.UpdateFile (which re-Renders the
// whole file). Hand-authored cards (source "manual" or no provenance) are left
// alone — re-Rendering could reformat prose the operator wrote by hand (C4).
func culiEditable(c knowledge.Card) bool {
	if c.Provenance == nil {
		return false
	}
	switch c.Provenance.Source {
	case "learn", "mcp", "import":
		return true
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// handleCardEdit applies a KB-detail edit to a culi-authored card: title,
// scope, key, and trigger keywords. It rewrites the card file via UpdateFile
// (preserving the body), re-syncs the index, and commits. Hand-authored cards
// are refused with a note rather than silently reformatted.
func (s *server) handleCardEdit(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	var req struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Scope    string `json:"scope"`
		Key      string `json:"key"`
		Keywords string `json:"keywords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := r.Context()
	sc, err := s.store.CardByID(ctx, req.ID)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
		return
	}
	fc, err := knowledge.ReadCard(s.kdir, sc.Path)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !culiEditable(fc) {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"ok":   false,
			"note": "hand-authored card — edit its file directly to preserve its formatting",
		})
		return
	}
	if err := knowledge.UpdateFile(s.kdir, sc.Path, func(c *knowledge.Card) {
		if t := strings.TrimSpace(req.Title); t != "" {
			c.Title = t
		}
		if scopes := strings.Fields(req.Scope); len(scopes) > 0 {
			c.Scopes = scopes
		}
		c.Key = strings.TrimSpace(req.Key)
		c.Triggers.Keywords = splitCSV(req.Keywords)
	}); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := knowledge.Commit(s.kdir, "card: edited "+sc.ID); err != nil {
		log.Printf("serve: commit after edit %s: %v", sc.ID, err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEdit acknowledges the inline quick-fix. The console applies the edit
// optimistically client-side; a server-side card writer (full editor) is not
// wired in this build, so this endpoint is intentionally a no-op that reports
// so honestly rather than silently dropping the edit.
func (s *server) handleEdit(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"note": "quick-fix applied in the console only; the full card editor is not yet wired",
	})
}

// repoInfo describes one watched repo for the manager popup.
type repoInfo struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	IsGit  bool   `json:"isGit"`
}

func repoInfos(repos []string) []repoInfo {
	out := make([]repoInfo, 0, len(repos))
	for _, p := range repos {
		info, err := os.Stat(p)
		exists := err == nil && info.IsDir()
		_, gerr := os.Stat(filepath.Join(p, ".git"))
		out = append(out, repoInfo{Path: p, Name: filepath.Base(p), Exists: exists, IsGit: gerr == nil})
	}
	return out
}

// dedupRepos trims, drops blanks, and removes duplicates while keeping order.
func dedupRepos(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func (s *server) handleReposGet(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, repoInfos(s.config().Repos))
}

// handleReposPost replaces the watched-repo list in config.yaml (comments
// preserved via config.SetRepos), reloads the server's config snapshot, and
// returns the new list with per-repo existence/git status.
func (s *server) handleReposPost(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	var req struct {
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	clean := dedupRepos(req.Repos)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := config.SetRepos(s.base, clean); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cfg, err := config.Load(s.base); err == nil {
		s.setConfig(cfg)
	}
	s.writeJSON(w, http.StatusOK, repoInfos(clean))
}

// handleConfigPost writes the whitelisted safe knobs into config.yaml,
// preserving the operator's comments and every unlisted key (C4). Only the
// keys config.SetKnobs recognizes are written; everything else in the body is
// ignored. Secrets never travel here — the token/key fields hold file paths.
func (s *server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	var patch map[string]string
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"saved": false, "note": "bad request body"})
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	applied, err := config.SetKnobs(s.base, patch)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"saved": false, "note": err.Error()})
		return
	}
	if cfg, lerr := config.Load(s.base); lerr == nil {
		s.setConfig(cfg)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"saved": true, "applied": applied})
}
