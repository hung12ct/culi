package branchgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Section is one culi-generated CLAUDE.md span. ID is the merge key and must
// be stable across runs; Markdown is the span's inner content.
type Section struct {
	ID       string
	Title    string
	Markdown string
}

// Marker grammar (contract C4: regeneration touches ONLY these spans).
// The recorded hash fingerprints the inner content culi last wrote — a
// mismatch at merge time means the user edited inside the span, and culi
// backs off.
// \r?\n keeps the grammar CRLF-tolerant — a CRLF CLAUDE.md must still match
// its own spans or every regen appends duplicates (reviewer-caught).
var spanRe = regexp.MustCompile(
	`(?ms)^<!-- culi:begin id=([A-Za-z0-9_-]+) hash=([0-9a-f]+) -->\r?\n(.*?)^<!-- culi:end id=[A-Za-z0-9_-]+ -->\r?\n?`)

// mergeClaudeMD splices new sections into existing content. Rules, in order,
// per section:
//   - span absent            → append at end
//   - user edited inside     → leave bytes, report conflict
//   - content unchanged      → leave bytes (byte-identical output)
//   - content changed        → replace the span
//
// Spans present in the file but absent from secs are left untouched — culi
// never deletes. Content outside markers is never modified.
func mergeClaudeMD(existing string, secs []Section) (out string, applied, conflicts []string) {
	type span struct {
		start, end   int
		recordedHash string
		inner        string
	}
	spans := map[string]span{}
	for _, m := range spanRe.FindAllStringSubmatchIndex(existing, -1) {
		id := existing[m[2]:m[3]]
		spans[id] = span{
			start: m[0], end: m[1],
			recordedHash: existing[m[4]:m[5]],
			inner:        existing[m[6]:m[7]],
		}
	}

	replacements := map[string]string{} // span id → new full span text
	var appendix strings.Builder
	for _, s := range secs {
		content := strings.TrimSpace(s.Markdown)
		if s.ID == "" || content == "" {
			continue
		}
		rendered := renderSpan(s.ID, content)
		sp, exists := spans[s.ID]
		if !exists {
			appendix.WriteString("\n" + rendered)
			applied = append(applied, s.ID+" (new)")
			continue
		}
		switch curHash := hash8(sp.inner); {
		case curHash != sp.recordedHash:
			conflicts = append(conflicts, s.ID)
		case curHash == hash8(content):
			// byte-stable: nothing to do
		default:
			replacements[s.ID] = rendered
			applied = append(applied, s.ID)
		}
	}

	if len(replacements) == 0 && appendix.Len() == 0 {
		return existing, applied, conflicts
	}
	var b strings.Builder
	last := 0
	for _, m := range spanRe.FindAllStringSubmatchIndex(existing, -1) {
		id := existing[m[2]:m[3]]
		rep, ok := replacements[id]
		if !ok {
			continue
		}
		b.WriteString(existing[last:m[0]])
		b.WriteString(rep)
		last = m[1]
	}
	b.WriteString(existing[last:])
	out = b.String()
	if appendix.Len() > 0 {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += appendix.String()
	}
	return out, applied, conflicts
}

// renderSpan wraps content in markers, hashing the exact inner bytes so the
// next merge can detect user edits.
func renderSpan(id, content string) string {
	inner := content + "\n"
	return fmt.Sprintf("<!-- culi:begin id=%s hash=%s -->\n%s<!-- culi:end id=%s -->\n",
		id, hash8(inner), inner, id)
}

// hash8 fingerprints span content — line endings normalized and whitespace
// trimmed so CRLF conversion or incidental newline drift does not read as a
// user edit.
func hash8(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:4])
}

// writeClaudeMD atomically replaces path (temp + rename); a torn write must
// never truncate the user's CLAUDE.md (C4).
func writeInstructionFile(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".culi-instructions-*")
	if err != nil {
		return fmt.Errorf("branchgen: creating temporary %s: %w", filepath.Base(path), err)
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("branchgen: writing %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("branchgen: closing temporary %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return fmt.Errorf("branchgen: chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("branchgen: replacing %s: %w", filepath.Base(path), err)
	}
	return nil
}
