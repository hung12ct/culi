package serve

import (
	"io/fs"
	"strings"
	"testing"
)

func TestConsoleUXContracts(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		raw, err := fs.ReadFile(assetFS, path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	js := read("assets/app.js")
	seed := read("assets/seed.js")
	css := read("assets/app.css")
	html := read("assets/index.html")

	for _, want := range []string{"Context Control", "context control"} {
		if !strings.Contains(html, want) {
			t.Errorf("index missing %q", want)
		}
	}
	if !strings.Contains(html, `id="s-version"`) {
		t.Error("status strip must expose the running Culi version")
	}
	if !strings.Contains(js, `set('s-version', st.version || '—')`) {
		t.Error("status rendering must populate the running Culi version")
	}
	if !strings.Contains(seed, `version: 'v0.0.0-dev'`) {
		t.Error("standalone console data must include a representative version")
	}
	for _, want := range []string{"culi-theme", `data-act="toggleTheme"`, "theme-moon", "theme-sun"} {
		if !strings.Contains(html, want) {
			t.Errorf("theme control missing %q", want)
		}
	}
	if !strings.Contains(js, `data-act="editCandidate"`) {
		t.Error("review must route edits to the persistent card editor")
	}
	if strings.Contains(js, `/api/candidates/${encodeURIComponent(cur.id)}/edit`) {
		t.Error("review still exposes the browser-only candidate edit endpoint")
	}
	if !strings.Contains(js, "No injection history yet") {
		t.Error("home must explain the empty injection-history state")
	}
	if !strings.Contains(js, "state.candidates ? state.candidates.length : (st.toReview || 0)") {
		t.Error("review navigation badge must use the status count before the queue is loaded")
	}
	if !strings.Contains(js, "uiIcon(n.icon)") {
		t.Error("navigation must use the product SVG icon system")
	}
	for _, want := range []string{"function toggleTheme()", "localStorage.setItem(THEME_KEY, theme)", "prefers-color-scheme: dark"} {
		if !strings.Contains(js, want) {
			t.Errorf("theme behavior missing %q", want)
		}
	}
	for _, want := range []string{"Knowledge Pulse", "screenKnowledgeAnalytics", "/api/analytics", "analyticsHarness", `data-change="analyticsRepo"`} {
		if !strings.Contains(js, want) {
			t.Errorf("knowledge analytics UI missing %q", want)
		}
	}
	for _, want := range []string{"function effBadge", "<th>Verdict</th>", "pulse-verdicts", "c.eff ? effBadge(c.eff.bucket, c.eff.note)"} {
		if !strings.Contains(js, want) {
			t.Errorf("effectiveness UI missing %q", want)
		}
	}
	for _, want := range []string{"bucket: 'helpful'", "bucket: 'noisy'", "eff('helpful')"} {
		if !strings.Contains(seed, want) {
			t.Errorf("standalone effectiveness data missing %q", want)
		}
	}
	for _, want := range []string{".eff-badge", ".pulse-verdicts"} {
		if !strings.Contains(css, want) {
			t.Errorf("effectiveness CSS missing %q", want)
		}
	}
	// Card history Revert must hit a real endpoint, not the old "Not wired" stub.
	if strings.Contains(js, "Not wired") {
		t.Error("revert must be wired to /api/cards/revert, not a toast stub")
	}
	for _, want := range []string{"async function revertCard", "/api/cards/revert", "'revert'"} {
		if !strings.Contains(js, want) {
			t.Errorf("revert wiring missing %q", want)
		}
	}
	// The inline edit form must be styled so Save/Cancel are visible.
	for _, want := range []string{".edit-btns", ".edit label", ".edit input"} {
		if !strings.Contains(css, want) {
			t.Errorf("edit-form CSS missing %q", want)
		}
	}
	for _, want := range []string{"Learning backend", "Codex terminal", "OpenAI API", "setLearningProvider", `data-key="openai_api_key_file"`, `data-act="setProvider:`} {
		if !strings.Contains(js+seed, want) {
			t.Errorf("learning settings UI missing %q", want)
		}
	}
	if !strings.Contains(js, "location.hash.replace") {
		t.Error("console must support hash deep links for Settings bookmarks")
	}
	for _, want := range []string{"production light visual system", "production dark visual system", "color-scheme: light", `html[data-theme="dark"]`, "--teal: #4f6fed"} {
		if !strings.Contains(css, want) {
			t.Errorf("production theme missing %q", want)
		}
	}
	for _, want := range []string{"@media (max-width: 760px)", ":focus-visible", "prefers-reduced-motion"} {
		if !strings.Contains(css, want) {
			t.Errorf("console CSS missing %q", want)
		}
	}
	for _, want := range []string{".pulse-chart", ".pulse-kpis", ".pulse-table", `.pulse-bar-segment.harness-codex`, `.pulse-bar-segment.harness-claude`} {
		if !strings.Contains(css, want) {
			t.Errorf("knowledge analytics CSS missing %q", want)
		}
	}
	for _, want := range []string{"Settings workspace", ".backend-grid", ".backend-option.selected", ".learning-model-grid", ".settings-grid"} {
		if !strings.Contains(css, want) {
			t.Errorf("settings CSS missing %q", want)
		}
	}
}
