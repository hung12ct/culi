package retrieve

import (
	"strings"
	"testing"
)

func TestGateAcks(t *testing.T) {
	acks := []string{
		"yes", "ok", "OK!", "  lgtm  ", "sounds good", "go ahead", "ship it",
		// Vietnamese, with and without the exact diacritics in the lexicon
		"tiếp tục", "tiep tuc", "được", "duoc", "làm đi", "cảm ơn", "đồng ý",
	}
	for _, p := range acks {
		if d := Gate(p, ""); !d.Skip || d.Reason != "ack" {
			t.Errorf("Gate(%q) = %+v, want ack skip", p, d)
		}
	}
}

func TestGateShortAndSlash(t *testing.T) {
	if d := Gate("/commit --pr", ""); !d.Skip || d.Reason != "slash" {
		t.Errorf("slash: %+v", d)
	}
	if d := Gate("fix it", ""); !d.Skip || d.Reason != "short" {
		t.Errorf("short: %+v", d)
	}
	if d := Gate("add rate limiting to the webhook endpoint", ""); d.Skip {
		t.Errorf("real prompt skipped: %+v", d)
	}
}

func TestGatePaste(t *testing.T) {
	// A huge log dump with no question: skip entirely.
	var b strings.Builder
	for range 300 {
		b.WriteString("2026-07-18T10:00:00Z ERROR something failed at pkg/foo.go:42\n")
		b.WriteString("\tgoroutine stack frame filler line\n")
	}
	if d := Gate(b.String(), ""); !d.Skip || d.Reason != "paste" {
		t.Errorf("log dump: %+v", d)
	}

	// Huge paste WITH a question: retrieve on truncated head+tail.
	withQ := "why does this fail?\n" + b.String()
	d := Gate(withQ, "")
	if d.Skip {
		t.Fatalf("paste with question skipped: %+v", d)
	}
	if len(d.Query) > pasteHead+pasteTail+8 {
		t.Errorf("query not truncated: %d bytes", len(d.Query))
	}
	if !strings.Contains(d.Query, "why does this fail?") {
		t.Errorf("head lost from truncated query")
	}
}

func TestGateNovelty(t *testing.T) {
	prev := "add rate limiting to the webhook endpoint handler"
	same := "add the rate limiting to webhook endpoint handler now"
	diff := "write a migration adding the invoices table with indexes"
	if d := Gate(same, prev); !d.Skip || d.Reason != "novelty" {
		t.Errorf("same topic: %+v", d)
	}
	if d := Gate(diff, prev); d.Skip {
		t.Errorf("different topic skipped: %+v", d)
	}
}

func TestFTSExprSanitizes(t *testing.T) {
	hostile := `how do I use "AND" OR NOT (a:b) * - in queries?`
	expr := FTSExpr(hostile)
	for _, bad := range []string{"(", ")", ":", "*", "-", `""`} {
		if strings.Contains(expr, bad) {
			t.Errorf("expr contains %q: %s", bad, expr)
		}
	}
	if expr == "" {
		t.Error("hostile input should still yield terms")
	}
	if FTSExpr("!!! ??? ...") != "" {
		t.Error("pure punctuation should yield empty expr")
	}
	// Terms are folded: Vietnamese diacritics removed to match FTS tokenizer.
	if got := FTSExpr("xử lý lỗi"); !strings.Contains(got, `"lo"`) && !strings.Contains(got, `"loi"`) {
		t.Errorf("vi terms not folded: %s", got)
	}
}

func TestFoldMultilingual(t *testing.T) {
	// The fold table must be language-neutral across Latin scripts, agreeing
	// with FTS5's remove_diacritics.
	tests := map[string]string{
		"café crème":   "cafe creme",   // fr
		"über größer":  "uber grosser", // de (ß → ss)
		"señor niño":   "senor nino",   // es
		"ação padrão":  "acao padrao",  // pt
		"żółć łódź":    "zolc lodz",    // pl
		"xử lý lỗi":    "xu ly loi",    // vi
		"naïve résumé": "naive resume",
	}
	for in, want := range tests {
		if got := fold(in); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtendLexicon(t *testing.T) {
	if d := Gate("d'accord", ""); d.Reason == "ack" {
		t.Fatal("phrase unexpectedly in built-in lexicon")
	}
	ExtendLexicon([]string{"d'accord"}, nil)
	if d := Gate("D'ACCORD!", ""); !d.Skip || d.Reason != "ack" {
		t.Errorf("extended ack not honored: %+v", d)
	}
}

func TestTermsAndJaccard(t *testing.T) {
	ts := terms("The webhook endpoint handler, with rate-limiting!", 0)
	joined := strings.Join(ts, " ")
	for _, want := range []string{"webhook", "endpoint", "handler", "rate", "limiting"} {
		if !strings.Contains(joined, want) {
			t.Errorf("terms missing %q: %v", want, ts)
		}
	}
	if strings.Contains(joined, "the") {
		t.Errorf("stopword leaked: %v", ts)
	}
	if j := jaccard([]string{"a1", "b1"}, []string{"a1", "b1"}); j != 1 {
		t.Errorf("identical jaccard = %f", j)
	}
	if j := jaccard([]string{"a1"}, []string{"zz"}); j != 0 {
		t.Errorf("disjoint jaccard = %f", j)
	}
}
