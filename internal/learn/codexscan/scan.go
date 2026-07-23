// Package codexscan discovers Codex rollout transcripts from Codex's local
// state databases. The databases are opened read-only and query-only: Culi
// never migrates, locks for writing, or otherwise owns Codex state.
package codexscan

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Session is one rollout-backed Codex thread ready for the neutral learning
// queue. SessionID is raw here; the queue boundary adds the harness prefix.
type Session struct {
	SessionID   string
	RolloutPath string
	CWD         string
	UpdatedAt   time.Time
}

// Discover reads every state_*.sqlite database newest-schema-first. Results
// are de-duplicated by rollout path because an upgrade may copy threads into a
// newer state database while retaining the older file. skipped counts rollouts
// dropped by the containment guard (safeRollout) so the caller can surface an
// otherwise-silent drop — e.g. a Codex build that relocates rollouts outside
// $CODEX_HOME would return sessions=0, skipped>0 instead of a mute empty scan.
func Discover(ctx context.Context, codexHome string) (sessions []Session, skipped int, err error) {
	if strings.TrimSpace(codexHome) == "" {
		return nil, 0, fmt.Errorf("codexscan: empty Codex home")
	}
	home, err := filepath.Abs(codexHome)
	if err != nil {
		return nil, 0, fmt.Errorf("codexscan: resolving Codex home: %w", err)
	}
	dbs, err := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	if err != nil {
		return nil, 0, fmt.Errorf("codexscan: finding state databases: %w", err)
	}
	sort.Slice(dbs, func(i, j int) bool { return stateVersion(dbs[i]) > stateVersion(dbs[j]) })
	if len(dbs) == 0 {
		return nil, 0, fmt.Errorf("codexscan: no state_*.sqlite database in %s", home)
	}

	seen := map[string]bool{}
	var out []Session
	var queryErr error
	queried := false
	for _, path := range dbs {
		found, dropped, err := scanDB(ctx, path, home)
		if err != nil {
			queryErr = err
			continue // older Codex state schemas may not have threads
		}
		queried = true
		skipped += dropped
		for _, session := range found {
			if seen[session.RolloutPath] {
				continue
			}
			seen[session.RolloutPath] = true
			out = append(out, session)
		}
	}
	if !queried {
		return nil, 0, queryErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, skipped, nil
}

func scanDB(ctx context.Context, path, home string) (found []Session, skipped int, err error) {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, 0, fmt.Errorf("codexscan: opening %s: %w", filepath.Base(path), err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, `
		SELECT id, rollout_path, cwd, updated_at
		FROM threads
		WHERE rollout_path <> ''
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, 0, fmt.Errorf("codexscan: querying %s: %w", filepath.Base(path), err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var id, rollout, cwd string
		var updated int64
		if err := rows.Scan(&id, &rollout, &cwd, &updated); err != nil {
			return nil, 0, fmt.Errorf("codexscan: reading %s: %w", filepath.Base(path), err)
		}
		if id == "" {
			continue
		}
		safe, ok := safeRollout(home, rollout)
		if !ok {
			// A non-empty rollout_path that fails containment is a real drop
			// (outside $CODEX_HOME, missing, or not a regular file), not noise.
			skipped++
			continue
		}
		out = append(out, Session{
			SessionID: id, RolloutPath: safe, CWD: cwd,
			UpdatedAt: time.Unix(updated, 0).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("codexscan: iterating %s: %w", filepath.Base(path), err)
	}
	return out, skipped, nil
}

func safeRollout(home, path string) (string, bool) {
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(realHome, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	st, err := os.Stat(realPath)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return realPath, true
}

func stateVersion(path string) int {
	name := strings.TrimSuffix(filepath.Base(path), ".sqlite")
	n, _ := strconv.Atoi(strings.TrimPrefix(name, "state_"))
	return n
}
