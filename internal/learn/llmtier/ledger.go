package llmtier

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/llmgen"
)

// ErrCapped reports today's spend cap is reached. Workers treat it as "stop
// now, keep everything queued" — the queue drains after midnight UTC.
var ErrCapped = errors.New("llmtier: daily learning cap reached")

// ErrBackendUnavailable reports the learning backend could not be reached in a
// way retrying this run won't fix: the claude CLI is logged out, or the API key
// is missing/invalid. Workers stop the run and keep jobs queued (like
// ErrCapped) — and, crucially, Generate does NOT fold such a failure into the
// daily cap: it spent nothing and every queued job would fail identically, so
// counting it would let a logged-out CLI burn the whole daily_call_cap and
// block learning even after the user logs back in.
var ErrBackendUnavailable = errors.New("llmtier: learning backend unavailable")

// IsStop reports errors on which a worker should halt the current run and leave
// its jobs queued (never park them): the daily cap is reached, or the backend
// is unavailable. Both are transient with respect to the job itself.
func IsStop(err error) bool {
	return errors.Is(err, ErrCapped) || errors.Is(err, ErrBackendUnavailable)
}

// ledgerRetentionDays bounds spend.json growth.
const ledgerRetentionDays = 90

// DaySpend is one UTC day's usage bucket.
type DaySpend struct {
	Calls      int     `json:"calls"`
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	USD        float64 `json:"usd"`
}

// Ledger is the persistent daily spend record (state/spend.json).
type Ledger struct {
	path string
	Days map[string]DaySpend `json:"days"`
}

// LoadLedger reads the ledger; missing or corrupt files start empty (worst
// case today's caps reset — bounded by one day's caps, never unbounded).
func LoadLedger(stateDir string) *Ledger {
	l := &Ledger{path: filepath.Join(stateDir, "spend.json"), Days: map[string]DaySpend{}}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return l
	}
	_ = json.Unmarshal(raw, l)
	if l.Days == nil {
		l.Days = map[string]DaySpend{}
	}
	return l
}

// Allow returns ErrCapped (wrapped with the reason) when today's bucket is at
// either cap. Caps ≤ 0 mean "no cap of that kind".
func (l *Ledger) Allow(now time.Time, usdCap float64, callCap int) error {
	d := l.Days[day(now)]
	if callCap > 0 && d.Calls >= callCap {
		return fmt.Errorf("%w: %d calls today (learn.daily_call_cap %d)", ErrCapped, d.Calls, callCap)
	}
	if usdCap > 0 && d.USD >= usdCap {
		return fmt.Errorf("%w: $%.2f today (learn.daily_usd_cap $%.2f)", ErrCapped, d.USD, usdCap)
	}
	return nil
}

// Record folds one call into today's bucket (in memory; Save persists).
func (l *Ledger) Record(now time.Time, usage llmgen.Usage, usd float64) {
	k := day(now)
	d := l.Days[k]
	d.Calls++
	d.Prompt += usage.Prompt
	d.Completion += usage.Completion
	d.USD += usd
	l.Days[k] = d
}

// Save writes the ledger atomically, pruning buckets past retention.
func (l *Ledger) Save() error {
	cutoff := day(time.Now().UTC().AddDate(0, 0, -ledgerRetentionDays))
	for k := range l.Days {
		if k < cutoff {
			delete(l.Days, k)
		}
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("llmtier: marshaling ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("llmtier: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".spend-*")
	if err != nil {
		return fmt.Errorf("llmtier: creating temp ledger: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("llmtier: writing ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("llmtier: closing ledger: %w", err)
	}
	if err := os.Rename(name, l.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("llmtier: replacing ledger: %w", err)
	}
	return nil
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// estimateUSD converts token usage to approximate dollars for the Anthropic
// API backend. Rates are per million tokens, matched by model-name substring;
// unknown models use the most expensive known rate so the cap errs safe.
// Approximation is fine: this drives a stop-spending guard, not billing.
func estimateUSD(model string, u llmgen.Usage) float64 {
	in, out := 15.0, 75.0 // opus-class ceiling as the safe default
	switch {
	case strings.Contains(model, "haiku"):
		in, out = 1.0, 5.0
	case strings.Contains(model, "sonnet"):
		in, out = 3.0, 15.0
	}
	return float64(u.Prompt)*in/1e6 + float64(u.Completion)*out/1e6
}
