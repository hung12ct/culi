package serve

import (
	"html"
	"regexp"
	"strings"
)

// renderMarkdown converts the small markdown subset culi cards actually use
// (headings ####, paragraphs, unordered lists, inline `code`/**strong**/*em*)
// into sanitized HTML for the review console's .md-body styling.
//
// It is deliberately minimal — not a CommonMark engine. Everything is
// HTML-escaped first (injection hardening, plan §5: card bodies are derived
// from transcripts and treated as data), so unrecognized markdown degrades to
// escaped text rather than raw HTML. A fuller renderer (goldmark) is a future
// swap behind this one function if card bodies ever grow richer.
func renderMarkdown(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var b strings.Builder
	var list []string

	flushList := func() {
		if len(list) == 0 {
			return
		}
		b.WriteString("<ul>")
		for _, li := range list {
			b.WriteString("<li>" + inline(li) + "</li>")
		}
		b.WriteString("</ul>")
		list = nil
	}

	var para []string
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>")
		para = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flushList()
			flushPara()
		case isHeading(trimmed):
			flushList()
			flushPara()
			lvl, text := heading(trimmed)
			// Levels 1–2 render a shade larger (h3); 3–6 share the compact h4
			// style. The card title already appears above the body, so even a
			// leading "# Title" stays visually subordinate.
			tag := "h4"
			if lvl <= 2 {
				tag = "h3"
			}
			b.WriteString("<" + tag + ">" + inline(text) + "</" + tag + ">")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			flushPara()
			list = append(list, trimmed[2:])
		default:
			flushList()
			para = append(para, trimmed)
		}
	}
	flushList()
	flushPara()
	return b.String()
}

// isHeading reports whether trimmed is an ATX heading ("# ".."###### ").
func isHeading(trimmed string) bool {
	lvl, _ := heading(trimmed)
	return lvl > 0
}

// heading parses an ATX heading, returning its level (1–6) and text, or 0 if
// the line is not a heading (no #, more than 6 #, or no space after the #s).
func heading(trimmed string) (level int, text string) {
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(trimmed) || trimmed[i] != ' ' {
		return 0, ""
	}
	return i, strings.TrimSpace(trimmed[i+1:])
}

var (
	reCode   = regexp.MustCompile("`([^`]+)`")
	reStrong = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reEm     = regexp.MustCompile(`\*([^*]+)\*`)
)

// inline escapes then applies inline spans. Code is substituted first so
// asterisks inside code fences are not re-interpreted as emphasis.
func inline(s string) string {
	s = html.EscapeString(s)
	s = reCode.ReplaceAllString(s, "<code>$1</code>")
	s = reStrong.ReplaceAllString(s, "<strong>$1</strong>")
	s = reEm.ReplaceAllString(s, "<em>$1</em>")
	return s
}
