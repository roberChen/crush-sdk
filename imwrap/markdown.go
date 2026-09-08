package imwrap

import (
	"fmt"
	"regexp"
	"strings"
)

// markdownish renders a practical subset of markdown to safe HTML:
// fenced code blocks, headings, horizontal rules, block quotes,
// ordered and unordered lists, tables (with alignment), paragraphs
// with soft line breaks, and inline markup (bold, emphasis,
// strikethrough, inline code, links, images-as-links, bare-URL
// autolinks). Input is HTML-escaped before any markup is applied, so
// raw HTML never passes through.
func markdownish(s string) string {
	chunks := strings.Split(s, "```")
	var b strings.Builder
	for i, chunk := range chunks {
		if i%2 == 1 {
			fmt.Fprintf(&b, "<pre class=\"code\">%s</pre>", esc(trimEdgeNewlines(chunk)))
			continue
		}
		if i > 0 {
			// Drop the newline that immediately follows a closing
			// fence so it does not become a spurious blank line.
			chunk = strings.TrimPrefix(chunk, "\n")
		}
		b.WriteString(renderBlocks(chunk))
	}
	return b.String()
}

// trimEdgeNewlines removes at most one leading and one trailing
// newline from a fenced code block body.
func trimEdgeNewlines(s string) string {
	s = strings.TrimPrefix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	return s
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	// RE2 has no backreferences, so the three hr shapes are separate
	// alternatives of a single repeated character.
	reHR     = regexp.MustCompile(`^(?:-\s*){3,}$|^(?:\*\s*){3,}$|^(?:_\s*){3,}$`)
	reULItem = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	reOLItem = regexp.MustCompile(`^\d{1,9}[.)]\s+(.*)$`)
	reQuote  = regexp.MustCompile(`^>\s?(.*)$`)
)

// isTableSeparator matches the `| --- | :---: |` line under a table
// header.
var reTableSeparator = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)

// renderBlocks parses block-level structure from plain (unescaped)
// markdown text.
func renderBlocks(text string) string {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	var b strings.Builder
	var para []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		body := inlineNoBreak(strings.Join(para, "\n"))
		body = strings.ReplaceAll(body, "\n", "<br>")
		fmt.Fprintf(&b, "<p>%s</p>", body)
		para = nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			flushPara()

		case reHeading.MatchString(trimmed):
			flushPara()
			m := reHeading.FindStringSubmatch(trimmed)
			level := len(m[1])
			fmt.Fprintf(&b, "<h%d>%s</h%d>", level, inlineNoBreak(strings.TrimSpace(m[2])), level)

		case reHR.MatchString(trimmed):
			flushPara()
			b.WriteString("<hr>")

		case strings.Contains(trimmed, "|") && i+1 < len(lines) && reTableSeparator.MatchString(lines[i+1]):
			flushPara()
			var rows []string
			j := i
			for ; j < len(lines); j++ {
				candidate := strings.TrimSpace(lines[j])
				if j > i+1 && (!strings.Contains(candidate, "|") || candidate == "") {
					break
				}
				if j == i+1 && !reTableSeparator.MatchString(lines[j]) {
					break
				}
				rows = append(rows, candidate)
			}
			// rows[0] header, rows[1] separator, rest body.
			aligns := parseTableAligns(rows[1])
			fmt.Fprintf(&b, "<table class=\"md\"><thead><tr>")
			for c, cell := range splitTableRow(rows[0]) {
				fmt.Fprintf(&b, "<th%s>%s</th>", alignAttr(aligns, c), inlineNoBreak(cell))
			}
			b.WriteString("</tr></thead><tbody>")
			for _, row := range rows[2:] {
				b.WriteString("<tr>")
				for c, cell := range splitTableRow(row) {
					fmt.Fprintf(&b, "<td%s>%s</td>", alignAttr(aligns, c), inlineNoBreak(cell))
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</tbody></table>")
			i = j - 1

		case reQuote.MatchString(line):
			flushPara()
			var quoted []string
			for ; i < len(lines); i++ {
				m := reQuote.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				quoted = append(quoted, m[1])
			}
			inner := renderBlocks(strings.Join(quoted, "\n"))
			fmt.Fprintf(&b, "<blockquote>%s</blockquote>", inner)
			i--

		case reULItem.MatchString(trimmed):
			flushPara()
			var items []string
			for ; i < len(lines); i++ {
				m := reULItem.FindStringSubmatch(strings.TrimSpace(lines[i]))
				if m == nil {
					break
				}
				items = append(items, "<li>"+inlineNoBreak(strings.TrimSpace(m[1]))+"</li>")
			}
			fmt.Fprintf(&b, "<ul>%s</ul>", strings.Join(items, ""))
			i--

		case reOLItem.MatchString(trimmed):
			flushPara()
			var items []string
			for ; i < len(lines); i++ {
				m := reOLItem.FindStringSubmatch(strings.TrimSpace(lines[i]))
				if m == nil {
					break
				}
				items = append(items, "<li>"+inlineNoBreak(strings.TrimSpace(m[1]))+"</li>")
			}
			fmt.Fprintf(&b, "<ol>%s</ol>", strings.Join(items, ""))
			i--

		default:
			para = append(para, line)
		}
	}
	flushPara()
	return b.String()
}

// parseTableAligns reads the alignment markers of the separator row.
func parseTableAligns(separator string) []string {
	cells := splitTableRow(separator)
	aligns := make([]string, len(cells))
	for i, cell := range cells {
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			aligns[i] = "center"
		case right:
			aligns[i] = "right"
		default:
			aligns[i] = "left"
		}
	}
	return aligns
}

func alignAttr(aligns []string, col int) string {
	if col >= len(aligns) || aligns[col] == "left" {
		return ""
	}
	return fmt.Sprintf(" style=\"text-align:%s\"", aligns[col])
}

// splitTableRow splits a table row into cells on unescaped pipes.
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		p = strings.ReplaceAll(p, `\|`, "|")
		out[i] = strings.TrimSpace(p)
	}
	return out
}

var (
	reInlineCode  = regexp.MustCompile("`([^`\n]+)`")
	reBold        = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reEmph        = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
	reStrike      = regexp.MustCompile(`~~([^~\n]+)~~`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	reImage       = regexp.MustCompile(`!\[([^\]]*)\]\((https?://[^)\s]+)\)`)
	reAutolink    = regexp.MustCompile(`(^|[\s(])(https?://[^\s<>()"]+)`)
	reTagOrEntity = regexp.MustCompile(`<[^>]*>|&[a-zA-Z#0-9]+;`)
)

// inlineNoBreak renders inline markup. The input is escaped first;
// already-generated tags and entities are protected from the
// autolinker by masking (see autolinkSafe).
func inlineNoBreak(s string) string {
	s = esc(s)
	s = reImage.ReplaceAllString(s, `<a href="$2" target="_blank" rel="noreferrer">🖼 $1</a>`)
	s = reLink.ReplaceAllString(s, `<a href="$2" target="_blank" rel="noreferrer">$1</a>`)
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reEmph.ReplaceAllString(s, "$1<em>$2</em>")
	s = reStrike.ReplaceAllString(s, "<del>$1</del>")
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	return autolinkSafe(s)
}

// autolinkSafe scans text, skipping regions inside tags or entities,
// and linkifies bare URLs in plain runs.
func autolinkSafe(s string) string {
	var b strings.Builder
	pos := 0
	for pos < len(s) {
		loc := reTagOrEntity.FindStringIndex(s[pos:])
		if loc == nil {
			b.WriteString(linkifyRun(s[pos:]))
			break
		}
		b.WriteString(linkifyRun(s[pos : pos+loc[0]]))
		b.WriteString(s[pos+loc[0] : pos+loc[1]])
		pos += loc[1]
	}
	return b.String()
}

func linkifyRun(s string) string {
	var b strings.Builder
	pos := 0
	for {
		loc := reAutolink.FindStringIndex(s[pos:])
		if loc == nil {
			b.WriteString(s[pos:])
			return b.String()
		}
		start, end := pos+loc[0], pos+loc[1]
		// The regex consumes one boundary character before the URL;
		// keep the boundary plain and linkify from "http".
		urlStart := start + strings.Index(s[start:end], "http")
		b.WriteString(s[start:urlStart])
		fmt.Fprintf(&b, `<a href="%s" target="_blank" rel="noreferrer">%s</a>`, s[urlStart:end], s[urlStart:end])
		pos = end
	}
}
