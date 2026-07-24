// Package serve hosts the culi review console: a local, single-user web UI
// over the knowledge store (candidate triage, KB browse, token-savings and
// job-health dashboards). It is off every performance-critical path — the hook
// and MCP surfaces never touch it — so it favors clarity over the hot-path
// stdlib-only discipline the retrieval packages keep.
//
// The frontend is embedded (assets.go) and served static; all dynamic data
// flows through /api/* JSON endpoints backed by the same store, knowledge, and
// learn packages the CLI uses. Writes (approve/reject) reuse mine.Confirm /
// mine.Reject verbatim, so the console and `culi review` stay behaviorally
// identical and every mutation lands in the knowledge git history.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/store"
)

// Options carries everything Run needs; the caller (cli.Serve) owns the store
// and config lifecycle.
type Options struct {
	Base    string
	Cfg     config.Config
	Store   *store.Store
	Addr    string
	Version string
	// StatsFn returns the `culi stats` report (as an opaque value the console
	// re-marshals to JSON), injected so serve need not import the cli package.
	StatsFn func(context.Context) any
}

// server holds request-scoped dependencies. A single mutex serializes the few
// write endpoints (approve/reject) and their follow-up index syncs; reads run
// concurrently on the WAL-backed store.
type server struct {
	base    string
	kdir    string
	addr    string
	version string
	store   *store.Store
	statsFn func(context.Context) any
	writeMu sync.Mutex

	cfgMu sync.RWMutex // guards cfg (reloaded when repos are edited)
	cfg   config.Config
}

// config returns the current config snapshot under a read lock.
func (s *server) config() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// setConfig swaps in a freshly loaded config (after a config.yaml write).
func (s *server) setConfig(c config.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = c
}

// Run starts the console HTTP server and blocks until ctx is cancelled or the
// listener fails. Shutdown is graceful (in-flight requests drain).
func Run(ctx context.Context, opt Options) error {
	if opt.Store == nil {
		return errors.New("serve: nil store")
	}
	addr := opt.Addr
	if addr == "" {
		addr = "localhost:7378"
	}
	s := &server{
		base:    opt.Base,
		kdir:    config.KnowledgeDir(opt.Base),
		addr:    addr,
		version: opt.Version,
		cfg:     opt.Cfg,
		store:   opt.Store,
		statsFn: opt.StatsFn,
	}

	sub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		return fmt.Errorf("serve: mounting assets: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/analytics", s.handleAnalytics)
	mux.HandleFunc("GET /api/candidates", s.handleCandidates)
	mux.HandleFunc("POST /api/candidates/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/candidates/{id}/reject", s.handleReject)
	mux.HandleFunc("GET /api/cards", s.handleCards)
	mux.HandleFunc("POST /api/cards/action", s.handleCardAction)
	mux.HandleFunc("POST /api/cards/edit", s.handleCardEdit)
	mux.HandleFunc("POST /api/cards/revert", s.handleCardRevert)
	mux.HandleFunc("GET /api/cards/{id}", s.handleCard)
	mux.HandleFunc("GET /api/activity/injections", s.handleInjections)
	mux.HandleFunc("GET /api/activity/runs", s.handleRuns)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config", s.handleConfigPost)
	mux.HandleFunc("GET /api/repos", s.handleReposGet)
	mux.HandleFunc("POST /api/repos", s.handleReposPost)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("culi serve — review console on http://%s", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
