// Package style implements learning pipeline B: periodic synthesis of the
// per-session style observations that the miner appends to
// state/style_observations.jsonl. Observations are raw signal, never cards —
// a card is synthesized only for a preference with at least three consistent
// observations across at least two sessions (structurally one-off-proof,
// plan §B). Zero qualifying groups ⇒ zero LLM calls.
package style

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Synthesis policy: fire weekly, or early when enough new observations
// accumulate. Group qualification is the plan's ≥3-observations/≥2-sessions
// consistency gate.
const (
	synthInterval  = 7 * 24 * time.Hour
	synthNewObs    = 15
	minGroupObs    = 3
	minGroupSess   = 2
	maxLedgerRows  = 500 // synthesis always reads the ledger tail, capped
	maxInputBytes  = 10 << 10
	maxGroupRender = 12 // observations rendered per group
)

// Observation is one ledger row (written by internal/learn/mine).
type Observation struct {
	TS          string `json:"ts"`
	SessionID   string `json:"session_id"`
	Repo        string `json:"repo"`
	Dimension   string `json:"dimension"`
	Observation string `json:"observation"`
	Evidence    string `json:"evidence"`
}

// loadLedger reads the observations ledger tail (last maxLedgerRows rows) and
// the total row count. Malformed rows are skipped, never fatal.
func loadLedger(stateDir string) (rows []Observation, total int, err error) {
	f, err := os.Open(filepath.Join(stateDir, "style_observations.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("style: opening ledger: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var o Observation
		if json.Unmarshal(sc.Bytes(), &o) != nil || strings.TrimSpace(o.Observation) == "" {
			continue
		}
		total++
		rows = append(rows, o)
		if len(rows) > maxLedgerRows {
			rows = rows[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return rows, total, fmt.Errorf("style: reading ledger: %w", err)
	}
	return rows, total, nil
}

// synthState tracks the trigger counter (state/style_state.json).
type synthState struct {
	Consumed int       `json:"consumed"` // ledger rows already counted by a synthesis run
	LastAt   time.Time `json:"last_at"`
}

func statePath(stateDir string) string { return filepath.Join(stateDir, "style_state.json") }

func loadState(stateDir string) synthState {
	var st synthState
	raw, err := os.ReadFile(statePath(stateDir))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

func saveState(stateDir string, st synthState) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("style: marshaling state: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("style: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, ".style-*")
	if err != nil {
		return fmt.Errorf("style: creating temp state: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("style: writing state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("style: closing state: %w", err)
	}
	if err := os.Rename(name, statePath(stateDir)); err != nil {
		os.Remove(name)
		return fmt.Errorf("style: replacing state: %w", err)
	}
	return nil
}

// due reports whether the synthesis trigger fires: enough new observations,
// or any new ones after a full interval.
func due(st synthState, total int, now time.Time) bool {
	fresh := total - st.Consumed
	if fresh <= 0 {
		return false
	}
	return fresh >= synthNewObs || now.Sub(st.LastAt) >= synthInterval
}

// group is one qualifying (repo, dimension) bucket.
type group struct {
	Repo      string
	Dimension string
	Rows      []Observation
	Sessions  int
}

// qualifyGroups buckets observations and keeps only groups meeting the
// consistency gate.
func qualifyGroups(rows []Observation) []group {
	type key struct{ repo, dim string }
	buckets := map[key][]Observation{}
	for _, o := range rows {
		k := key{o.Repo, strings.ToLower(strings.TrimSpace(o.Dimension))}
		buckets[k] = append(buckets[k], o)
	}
	var out []group
	for k, rs := range buckets {
		sessions := map[string]bool{}
		for _, o := range rs {
			sessions[o.SessionID] = true
		}
		if len(rs) >= minGroupObs && len(sessions) >= minGroupSess {
			out = append(out, group{Repo: k.repo, Dimension: k.dim, Rows: rs, Sessions: len(sessions)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Rows) != len(out[j].Rows) {
			return len(out[i].Rows) > len(out[j].Rows)
		}
		return out[i].Repo+out[i].Dimension < out[j].Repo+out[j].Dimension
	})
	return out
}

// renderGroups serializes qualifying groups for the model, byte-capped.
func renderGroups(groups []group) string {
	var b strings.Builder
	for _, g := range groups {
		var gb strings.Builder
		fmt.Fprintf(&gb, "### repo=%s dimension=%s (%d observations, %d sessions)\n",
			g.Repo, g.Dimension, len(g.Rows), g.Sessions)
		rows := g.Rows
		if len(rows) > maxGroupRender {
			rows = rows[len(rows)-maxGroupRender:]
		}
		for _, o := range rows {
			fmt.Fprintf(&gb, "- [%s] %s", o.SessionID, o.Observation)
			if o.Evidence != "" {
				fmt.Fprintf(&gb, " (evidence: %s)", o.Evidence)
			}
			gb.WriteString("\n")
		}
		gb.WriteString("\n")
		if b.Len()+gb.Len() > maxInputBytes {
			b.WriteString("(further groups truncated)\n")
			break
		}
		b.WriteString(gb.String())
	}
	return b.String()
}
