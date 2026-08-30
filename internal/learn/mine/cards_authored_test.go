package mine

import (
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
)

// Every provenance culi writes must be retirable. The branch pattern miner and
// culi gen were missing, which quietly froze 124 generated cards in place.
func TestCuliAuthoredCoversEveryProvenanceCuliWrites(t *testing.T) {
	for _, src := range []string{"learn", "mcp", "import", "patterns", "gen"} {
		if !culiAuthored(knowledge.Card{Provenance: &knowledge.Provenance{Source: src}}) {
			t.Errorf("source %q not recognised as culi-authored — nothing can retire those cards", src)
		}
	}
}

// A file the user wrote by hand still never round-trips through Render (C4).
func TestCuliAuthoredRejectsHandAuthored(t *testing.T) {
	if culiAuthored(knowledge.Card{}) {
		t.Error("card with no provenance treated as culi-authored")
	}
	if culiAuthored(knowledge.Card{Provenance: &knowledge.Provenance{Source: "hand"}}) {
		t.Error("unknown provenance treated as culi-authored")
	}
}
