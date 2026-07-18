package knowledge

import (
	"strings"
	"testing"
)

func TestRenderParseRoundTrip(t *testing.T) {
	in := Card{
		Path:    "rules/tricky.md",
		Type:    "rule",
		Title:   "Tricky: title with colon",
		Summary: `Summary with "quotes" and: colons — the case that once broke a hand-rolled writer`,
		Body:    "Line one.\n\nLine two with `code`.\n",
		Scopes:  []string{"repo:alpha", "lang:go"},
		Aliases: []string{"xử lý lỗi"},
		Export: &ExportMeta{
			Kind:        "skill",
			Name:        "tricky",
			Frontmatter: "description: multi\n  line: value",
			Attachments: []string{"extra.md"},
		},
		Provenance: &Provenance{Source: "import", MergedFrom: []string{"alpha", "beta"}, Model: "m"},
	}
	raw, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(in.Path, raw)
	if err != nil {
		t.Fatalf("rendered card failed to parse:\n%s\nerr: %v", raw, err)
	}
	if out.Title != in.Title || out.Summary != in.Summary {
		t.Errorf("title/summary drift: %q / %q", out.Title, out.Summary)
	}
	if strings.TrimSpace(out.Body) != strings.TrimSpace(in.Body) {
		t.Errorf("body drift: %q", out.Body)
	}
	if len(out.Scopes) != 2 || out.Scopes[0] != "repo:alpha" {
		t.Errorf("scopes drift: %v", out.Scopes)
	}
	if out.Export == nil || out.Export.Frontmatter != in.Export.Frontmatter || out.Export.Attachments[0] != "extra.md" {
		t.Errorf("export drift: %+v", out.Export)
	}
	if out.Provenance == nil || len(out.Provenance.MergedFrom) != 2 {
		t.Errorf("provenance drift: %+v", out.Provenance)
	}
	// Derivable id/type stay out of the frontmatter to keep files minimal.
	if strings.Contains(string(raw), "id:") || strings.Contains(string(raw), "\ntype:") {
		t.Errorf("redundant keys rendered:\n%s", raw)
	}
}

func TestRenderBodyStartingWithDashes(t *testing.T) {
	// A body whose first line is "---" must not corrupt the frontmatter split.
	in := Card{Path: "rules/x.md", Type: "rule", Title: "X", Summary: "s", Body: "---\nnot frontmatter\n---\ntext"}
	raw, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(in.Path, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Body, "not frontmatter") || out.Title != "X" {
		t.Errorf("parse after render: %+v", out)
	}
}
