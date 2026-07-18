package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// classify labels a cluster by how much its copies drifted:
//
//	unique    — present in one repo only (stays repo-scoped)
//	identical — all copies normalize to the same bytes
//	superset  — one copy's line set contains every other copy's lines
//	diverged  — real drift; needs the LLM merge
//
// Canonical is the repo whose copy merge stages mechanically (for diverged it
// is only the richest input, shown for orientation).
func classify(c Cluster) Cluster {
	if len(c.Items) == 1 {
		c.Class = "unique"
		c.Canonical = c.Items[0].Repo
		return c
	}
	c.AttachmentDrift = attachmentDrift(c.Items)

	identical := true
	for _, it := range c.Items[1:] {
		if it.NormHash != c.Items[0].NormHash {
			identical = false
			break
		}
	}
	if identical {
		c.Class = "identical"
		c.Canonical = c.Items[0].Repo
		return c
	}

	sets := make([]map[string]bool, len(c.Items))
	for i, it := range c.Items {
		sets[i] = lineSet(it.Path)
	}
	if i := supersetIndex(sets); i >= 0 {
		c.Class = "superset"
		c.Canonical = c.Items[i].Repo
		return c
	}

	c.Class = "diverged"
	c.Similarity = minPairwiseJaccard(sets)
	richest := 0
	for i, s := range sets {
		if len(s) > len(sets[richest]) {
			richest = i
		}
	}
	c.Canonical = c.Items[richest].Repo
	return c
}

// normalize reduces content to its non-blank lines with trailing whitespace
// and CR removed — blank-line and whitespace drift is formatting, not
// meaning, and classifies as identical. The canonical copy is staged with its
// real formatting either way; normalization only drives classification.
func normalize(raw []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimRight(l, " \t"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// lineSet loads a file's normalized non-blank lines as a set. Read errors
// yield an empty set — the file was readable moments ago in scan; a race here
// only downgrades the classification to diverged, never corrupts anything.
func lineSet(path string) map[string]bool {
	set := map[string]bool{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return set
	}
	for _, l := range normalize(raw) {
		if l != "" {
			set[l] = true
		}
	}
	return set
}

// supersetIndex returns the index of a set containing all others, or -1.
// Ties go to the largest set.
func supersetIndex(sets []map[string]bool) int {
	best := -1
	for i, s := range sets {
		ok := true
		for j, o := range sets {
			if j == i {
				continue
			}
			if !contains(s, o) {
				ok = false
				break
			}
		}
		if ok && (best < 0 || len(s) > len(sets[best])) {
			best = i
		}
	}
	return best
}

func contains(super, sub map[string]bool) bool {
	if len(sub) > len(super) {
		return false
	}
	for l := range sub {
		if !super[l] {
			return false
		}
	}
	return true
}

// minPairwiseJaccard reports the worst overlap between any two copies —
// the honest "how bad is the drift" number for the scan report.
func minPairwiseJaccard(sets []map[string]bool) float64 {
	minJ := 1.0
	for i := range sets {
		for j := i + 1; j < len(sets); j++ {
			if v := jaccardSets(sets[i], sets[j]); v < minJ {
				minJ = v
			}
		}
	}
	return minJ
}

func jaccardSets(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for l := range a {
		if b[l] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// attachmentDrift reports whether copies disagree on skill attachment lists.
// Content-level attachment diffing is out of scope for v1 — the list is the
// signal; the report flags it for manual review.
func attachmentDrift(items []Item) bool {
	first := strings.Join(items[0].Attachments, "\n")
	for _, it := range items[1:] {
		if strings.Join(it.Attachments, "\n") != first {
			return true
		}
	}
	return false
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
