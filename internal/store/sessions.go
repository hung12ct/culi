package store

import (
	"context"
	"fmt"
)

// InjectionRow is one logged card injection with its timestamp — the raw
// material the review console groups into the Activity "Injections" view.
// Read-only, off the hot path.
type InjectionRow struct {
	SessionID   string
	TS          string
	Event       string
	CardID      string
	Granularity string
	Tokens      int
	Cwd         string
}

// RecentInjections returns injection-log rows newest first, capped at limit.
// The console groups them by session then by (ts, event) in Go; keeping the
// grouping out of SQL avoids a bespoke query the hot path never needs.
func (s *Store) RecentInjections(ctx context.Context, limit int) ([]InjectionRow, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, ts, event, card_id, granularity, tokens, cwd
		FROM injections ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading recent injections: %w", err)
	}
	defer rows.Close()
	var out []InjectionRow
	for rows.Next() {
		var r InjectionRow
		if err := rows.Scan(&r.SessionID, &r.TS, &r.Event, &r.CardID, &r.Granularity, &r.Tokens, &r.Cwd); err != nil {
			return nil, fmt.Errorf("store: scanning injection row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DailyTokens returns injected-token totals per calendar day (UTC), oldest
// first, for up to the last `days` days present in the retention window — the
// Overview hero sparkline. Empty when nothing has been injected yet.
func (s *Store) DailyTokens(ctx context.Context, days int) ([]int, error) {
	if days <= 0 {
		days = 12
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(SUM(tokens), 0) AS tok
		FROM injections GROUP BY date(ts) ORDER BY date(ts) DESC LIMIT ?`, days)
	if err != nil {
		return nil, fmt.Errorf("store: reading daily tokens: %w", err)
	}
	defer rows.Close()
	var desc []int
	for rows.Next() {
		var t int
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("store: scanning daily tokens: %w", err)
		}
		desc = append(desc, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to oldest-first for the left-to-right sparkline.
	out := make([]int, len(desc))
	for i, v := range desc {
		out[len(desc)-1-i] = v
	}
	return out, nil
}
