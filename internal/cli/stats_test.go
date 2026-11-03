package cli

import "testing"

func TestComputeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		sessions, corpus, inj int
		wantPerSession        int
		wantShare             float64
	}{
		{"typical week", 28, 114579, 36934, 1319, 1.151},
		{"rounds to nearest", 3, 1000, 1000, 333, 33.3},
		{"no sessions", 0, 3000, 0, 0, 0},
		{"empty corpus", 10, 0, 500, 50, 0},
		{"injects more than the store (chatty, tiny corpus)", 2, 100, 900, 450, 450},
	} {
		d := computeDelivery(tc.sessions, tc.corpus, tc.inj)
		if d.PerSession != tc.wantPerSession || d.Injected != tc.inj || d.Sessions != tc.sessions {
			t.Errorf("%s: delivery = %+v", tc.name, d)
		}
		if diff := d.CorpusShare - tc.wantShare; diff > 0.01 || diff < -0.01 {
			t.Errorf("%s: share = %f, want %f", tc.name, d.CorpusShare, tc.wantShare)
		}
	}
}

func TestCompactTokens(t *testing.T) {
	for n, want := range map[int]string{0: "0", 850: "850", 1319: "1.3k", 9999: "10k", 12400: "12k"} {
		if got := compactTokens(n); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", n, got, want)
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
