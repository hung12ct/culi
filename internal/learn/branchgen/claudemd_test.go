package branchgen

import (
	"strings"
	"testing"
)

func TestMergeCreatesFromEmpty(t *testing.T) {
	out, applied, conflicts := mergeClaudeMD("", []Section{
		{ID: "module-map", Markdown: "## Modules\n\n- internal/a"},
		{ID: "build", Markdown: "## Build\n\nmake check"},
	})
	if len(applied) != 2 || len(conflicts) != 0 {
		t.Fatalf("applied=%v conflicts=%v", applied, conflicts)
	}
	for _, want := range []string{
		"<!-- culi:begin id=module-map hash=", "<!-- culi:end id=module-map -->",
		"## Build", "make check",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMergeIsByteStableWhenUnchanged(t *testing.T) {
	secs := []Section{{ID: "a", Markdown: "## A\n\ncontent"}}
	first, _, _ := mergeClaudeMD("", secs)
	second, applied, conflicts := mergeClaudeMD(first, secs)
	if second != first {
		t.Errorf("regen not byte-stable:\n%q\nvs\n%q", first, second)
	}
	if len(applied) != 0 || len(conflicts) != 0 {
		t.Errorf("applied=%v conflicts=%v, want none", applied, conflicts)
	}
}

func TestMergeReplacesChangedSpan(t *testing.T) {
	first, _, _ := mergeClaudeMD("# Repo\n\nHuman intro.\n", []Section{{ID: "a", Markdown: "old content"}})
	out, applied, _ := mergeClaudeMD(first, []Section{{ID: "a", Markdown: "new content"}})
	if len(applied) != 1 {
		t.Fatalf("applied = %v", applied)
	}
	if strings.Contains(out, "old content") || !strings.Contains(out, "new content") {
		t.Errorf("span not replaced:\n%s", out)
	}
	if !strings.Contains(out, "Human intro.") {
		t.Error("content outside markers was lost (C4)")
	}
}

func TestMergeNeverTouchesUserEditedSpan(t *testing.T) {
	first, _, _ := mergeClaudeMD("", []Section{{ID: "a", Markdown: "generated"}})
	edited := strings.Replace(first, "generated", "generated\nMY OWN NOTE", 1)
	out, applied, conflicts := mergeClaudeMD(edited, []Section{{ID: "a", Markdown: "regenerated"}})
	if out != edited {
		t.Errorf("user-edited span was modified (C4):\n%s", out)
	}
	if len(conflicts) != 1 || conflicts[0] != "a" {
		t.Errorf("conflicts = %v", conflicts)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %v", applied)
	}
}

func TestMergeAppendsNewSpanKeepsForeignSpans(t *testing.T) {
	existing, _, _ := mergeClaudeMD("intro\n", []Section{{ID: "old", Markdown: "old span"}})
	out, applied, _ := mergeClaudeMD(existing, []Section{{ID: "extra", Markdown: "extra span"}})
	// "old" is absent from the new output — culi never deletes it.
	if !strings.Contains(out, "old span") || !strings.Contains(out, "extra span") {
		t.Errorf("spans wrong:\n%s", out)
	}
	if len(applied) != 1 || applied[0] != "extra (new)" {
		t.Errorf("applied = %v", applied)
	}
}

func TestMergeMatchesCRLFSpans(t *testing.T) {
	secs := []Section{{ID: "a", Markdown: "content"}}
	first, _, _ := mergeClaudeMD("", secs)
	crlf := strings.ReplaceAll(first, "\n", "\r\n")

	// Unchanged content in a CRLF file: recognized (no duplicate append),
	// byte-stable.
	out, applied, conflicts := mergeClaudeMD(crlf, secs)
	if out != crlf || len(applied) != 0 || len(conflicts) != 0 {
		t.Errorf("CRLF unchanged: applied=%v conflicts=%v stable=%v", applied, conflicts, out == crlf)
	}
	if strings.Count(out, "culi:begin") != 1 {
		t.Errorf("CRLF span duplicated:\n%s", out)
	}

	// Changed content: span replaced, not appended.
	out2, applied2, _ := mergeClaudeMD(crlf, []Section{{ID: "a", Markdown: "new content"}})
	if len(applied2) != 1 || strings.Count(out2, "culi:begin") != 1 || !strings.Contains(out2, "new content") {
		t.Errorf("CRLF replace: applied=%v\n%s", applied2, out2)
	}
}

func TestMergeSkipsEmptySections(t *testing.T) {
	out, applied, _ := mergeClaudeMD("keep\n", []Section{
		{ID: "", Markdown: "no id"},
		{ID: "x", Markdown: "   "},
	})
	if out != "keep\n" || len(applied) != 0 {
		t.Errorf("out=%q applied=%v", out, applied)
	}
}
