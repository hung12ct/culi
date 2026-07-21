package mine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/store"
)

// defaultConfirmAt is the fallback observation count where a candidate becomes
// confirmed and starts injecting (plan §learning lifecycle). Overridable per
// install via learn.confirm_at → Miner.ConfirmAt.
const defaultConfirmAt = 2

// confirmThreshold is the effective observation count for confirmation: the
// configured Miner.ConfirmAt, or the default when unset/invalid.
func (m *Miner) confirmThreshold() int {
	if m.ConfirmAt >= 1 {
		return m.ConfirmAt
	}
	return defaultConfirmAt
}

// culiAuthored reports whether a card was written by culi (import, learning,
// MCP save_lesson) and may therefore be rewritten via knowledge.UpdateFile.
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

// writeCandidate creates a new candidate card file. Never overwrites (C4):
// slug collisions get a deterministic short-ID suffix, like save_lesson.
// confirmed reports whether the threshold is so low (learn.confirm_at ≤ 1) that
// the card is born confirmed and injecting on its first sighting.
func (m *Miner) writeCandidate(ctx context.Context, typ string, c cardOut, repo string) (id string, confirmed bool, err error) {
	scope := validScope(c.Scope, repo)
	kw := c.Keywords
	if len(kw) > 5 {
		kw = kw[:5]
	}
	body := strings.TrimSpace(c.Markdown)
	if ev := strings.TrimSpace(c.Evidence); ev != "" {
		body += "\n\n**Evidence:** " + firstLine(ev)
	}
	confirmed = m.confirmThreshold() <= 1 // one observation already meets it
	status := "candidate"
	if confirmed {
		status = "confirmed"
	}
	card := knowledge.Card{
		Type:         typ,
		Title:        strings.TrimSpace(c.Title),
		Summary:      strings.TrimSpace(c.Summary),
		Body:         body,
		Scopes:       []string{scope},
		Triggers:     knowledge.Triggers{Keywords: kw},
		Status:       status,
		Observations: 1,
		Supersedes:   m.validSupersedes(ctx, c.Supersedes),
		Provenance:   &knowledge.Provenance{Source: "learn", Model: m.Tier.Cheap.ModelName()},
	}

	rel := candidatePath(typ, slugify(card.Title))
	kdir := config.KnowledgeDir(m.Base)
	abs := filepath.Join(kdir, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err == nil {
		rel = strings.TrimSuffix(rel, ".md") + "-" + knowledge.ShortID(card.Title+body) + ".md"
		abs = filepath.Join(kdir, filepath.FromSlash(rel))
	}
	rendered, err := knowledge.Render(card)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", false, fmt.Errorf("mine: creating card dir: %w", err)
	}
	if err := os.WriteFile(abs, rendered, 0o644); err != nil {
		return "", false, fmt.Errorf("mine: writing card: %w", err)
	}
	m.logf("%s %s (%s)", status, rel, scope)
	return strings.TrimSuffix(filepath.ToSlash(rel), ".md"), confirmed, nil
}

// reinforce bumps an existing card's observation count; at confirmAt the card
// confirms (starts injecting) and its superseded predecessor retires. The
// strongest positive feedback there is (FeedbackReferenced) is recorded either
// way; the file is only rewritten when culi authored it.
func (m *Miner) reinforce(ctx context.Context, sc store.StoredCard, c cardOut, res *Result) error {
	_ = m.Store.AddFeedback(ctx, sc.ID, store.FeedbackReferenced, 1) // best-effort

	kdir := config.KnowledgeDir(m.Base)
	fileCard, err := knowledge.ReadCard(kdir, sc.Path)
	if err != nil || !culiAuthored(fileCard) {
		res.Notes = append(res.Notes, fmt.Sprintf("re-observed %s (hand-authored — recorded feedback only)", sc.ID))
		return nil
	}
	confirmed := false
	var supersedes string
	err = knowledge.UpdateFile(kdir, sc.Path, func(card *knowledge.Card) {
		wasCandidate := card.Status == "candidate"
		card.Observations++
		if card.Observations >= m.confirmThreshold() && wasCandidate {
			card.Status = "confirmed"
			confirmed = true
			supersedes = card.Supersedes
		}
		// Evidence accretes only while a candidate: a confirmed card that keeps
		// re-observing would otherwise grow its body (= injection tokens) one
		// line per session, forever.
		if ev := strings.TrimSpace(c.Evidence); ev != "" && wasCandidate {
			card.Body += "\n\n**Reinforced:** " + firstLine(ev)
		}
	})
	if err != nil {
		return err
	}
	res.Reinforced = append(res.Reinforced, sc.ID)
	if confirmed {
		res.Confirmed = append(res.Confirmed, sc.ID)
		if supersedes != "" {
			if retired, err := RetireCard(ctx, m.Store, kdir, supersedes); err == nil && retired {
				res.Retired = append(res.Retired, supersedes)
			}
		}
	}
	return nil
}

// RetireCard sets status: retired on a culi-authored card (shared with
// `culi review`, which retires superseded cards on approval). Returns false
// when the card is missing or hand-authored — never an error worth failing a
// mine over.
func RetireCard(ctx context.Context, s *store.Store, kdir, id string) (bool, error) {
	sc, err := s.CardByID(ctx, id)
	if err != nil {
		return false, nil // already gone
	}
	fileCard, err := knowledge.ReadCard(kdir, sc.Path)
	if err != nil || !culiAuthored(fileCard) {
		return false, nil
	}
	if err := knowledge.UpdateFile(kdir, sc.Path, func(card *knowledge.Card) {
		card.Status = "retired"
	}); err != nil {
		return false, err
	}
	return true, nil
}

// Confirm flips a candidate to confirmed (review approval or forced promote)
// and retires the card it supersedes. Returns the retired ID ("" if none).
func Confirm(ctx context.Context, s *store.Store, kdir, id string) (string, error) {
	sc, err := s.CardByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("mine: %w", err)
	}
	fileCard, err := knowledge.ReadCard(kdir, sc.Path)
	if err != nil {
		return "", err
	}
	if !culiAuthored(fileCard) {
		return "", fmt.Errorf("mine: %s is hand-authored — edit its status field directly instead", id)
	}
	var supersedes string
	if err := knowledge.UpdateFile(kdir, sc.Path, func(card *knowledge.Card) {
		card.Status = "confirmed"
		supersedes = card.Supersedes
	}); err != nil {
		return "", err
	}
	if supersedes == "" {
		return "", nil
	}
	retired, err := RetireCard(ctx, s, kdir, supersedes)
	if err != nil || !retired {
		return "", err
	}
	return supersedes, nil
}

// Reject retires a candidate (kept on disk, unindexed by retrieval).
func Reject(ctx context.Context, s *store.Store, kdir, id string) error {
	sc, err := s.CardByID(ctx, id)
	if err != nil {
		return fmt.Errorf("mine: %w", err)
	}
	fileCard, err := knowledge.ReadCard(kdir, sc.Path)
	if err != nil {
		return err
	}
	if !culiAuthored(fileCard) {
		return fmt.Errorf("mine: %s is hand-authored — edit its status field directly instead", id)
	}
	return knowledge.UpdateFile(kdir, sc.Path, func(card *knowledge.Card) {
		card.Status = "retired"
	})
}

// validSupersedes only passes through IDs that exist — the model must not
// retire cards by hallucination.
func (m *Miner) validSupersedes(ctx context.Context, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if _, err := m.Store.CardByID(ctx, id); err != nil {
		return ""
	}
	return id
}

// validScope normalizes the model's scope: global | lang:<x> | repo:<name>.
// Anything else collapses to the session repo (or global outside git).
func validScope(scope, repo string) string {
	scope = strings.TrimSpace(scope)
	switch {
	case scope == "global",
		strings.HasPrefix(scope, "lang:") && len(scope) > 5,
		strings.HasPrefix(scope, "repo:") && len(scope) > 5:
		return scope
	}
	if repo != "" {
		return "repo:" + repo
	}
	return "global"
}

func candidatePath(typ, slug string) string {
	if typ == "lesson" {
		return "lessons/" + time.Now().UTC().Format("2006") + "/" + slug + ".md"
	}
	return "rules/" + slug + ".md"
}

// styleObservation is one ledger row for pipeline B (weekly synthesis).
type styleObservation struct {
	TS          string `json:"ts"`
	SessionID   string `json:"session_id"`
	Repo        string `json:"repo"`
	Dimension   string `json:"dimension"`
	Observation string `json:"observation"`
	Evidence    string `json:"evidence"`
}

// appendStyleObservations appends rows to state/style_observations.jsonl —
// observations are never cards (plan §learning B); synthesis happens later
// with cross-session consistency requirements.
func appendStyleObservations(stateDir string, job queue.Job, repo string, obs []styleOut) (int, error) {
	if len(obs) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return 0, fmt.Errorf("mine: creating state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "style_observations.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("mine: opening style ledger: %w", err)
	}
	defer f.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	n := 0
	for _, o := range obs {
		if strings.TrimSpace(o.Observation) == "" {
			continue
		}
		raw, err := json.Marshal(styleObservation{
			TS: now, SessionID: job.SessionID, Repo: repo,
			Dimension: o.Dimension, Observation: o.Observation, Evidence: firstLine(o.Evidence),
		})
		if err != nil {
			continue
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return n, fmt.Errorf("mine: writing style ledger: %w", err)
		}
		n++
	}
	return n, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify mirrors save_lesson's slug rules.
func slugify(s string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if slug == "" {
		return "card"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
}
