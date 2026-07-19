package cli

import "testing"

func TestComputeCounterfactual(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		sessions, corpus, inj int
		wantSaved             int
		wantPercentLo, wantHi float64
		wantDump              int
	}{
		{"typical week", 20, 3000, 6000, 54000, 89, 91, 60000},
		{"no sessions", 0, 3000, 0, 0, 0, 0, 0},
		{"empty corpus", 10, 0, 500, -500, 0, 0, 0},
		{"culi costs more (tiny corpus, chatty)", 2, 100, 900, -700, -351, -349, 200},
	} {
		cf := computeCounterfactual(tc.sessions, tc.corpus, tc.inj)
		if cf.DumpTokens != tc.wantDump || cf.SavedTokens != tc.wantSaved {
			t.Errorf("%s: cf = %+v", tc.name, cf)
		}
		if tc.wantDump > 0 && (cf.SavedPercent < tc.wantPercentLo || cf.SavedPercent > tc.wantHi) {
			t.Errorf("%s: percent = %f", tc.name, cf.SavedPercent)
		}
		if tc.wantDump == 0 && cf.SavedPercent != 0 {
			t.Errorf("%s: percent should be 0 with no dump baseline, got %f", tc.name, cf.SavedPercent)
		}
	}
}

func TestSkipReason(t *testing.T) {
	if r, ok := skipReason("gate skip (ack) session=x"); !ok || r != "ack" {
		t.Errorf("got %q %v", r, ok)
	}
	if _, ok := skipReason("hook user-prompt-submit: some error"); ok {
		t.Error("non-skip line matched")
	}
}
