package imwrap

import (
	"fmt"
	"strings"
)

// maxDiffLines caps the LCS matrix; larger inputs fall back to plain
// before/after blocks.
const maxDiffLines = 2000

type diffOp int

const (
	diffCtx diffOp = iota
	diffDel
	diffAdd
)

type diffLine struct {
	op   diffOp
	text string
}

// splitLines splits text into lines, keeping each line's trailing
// newline so unchanged lines compare equal on both sides.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lineDiff computes a line-based diff between old and new via a
// longest-common-subsequence matrix. Inputs longer than maxDiffLines
// lines fall back to a whole-block replacement.
func lineDiff(oldText, newText string) []diffLine {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	if len(oldLines) > maxDiffLines || len(newLines) > maxDiffLines {
		out := make([]diffLine, 0, len(oldLines)+len(newLines))
		for _, l := range oldLines {
			out = append(out, diffLine{op: diffDel, text: l})
		}
		for _, l := range newLines {
			out = append(out, diffLine{op: diffAdd, text: l})
		}
		return out
	}

	n, m := len(oldLines), len(newLines)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}

	out := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, diffLine{op: diffCtx, text: oldLines[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{op: diffDel, text: oldLines[i]})
			i++
		default:
			out = append(out, diffLine{op: diffAdd, text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{op: diffDel, text: oldLines[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{op: diffAdd, text: newLines[j]})
	}
	return out
}

// diffStats summarizes a diff's added/removed line counts.
func diffStats(lines []diffLine) (added, removed int) {
	for _, l := range lines {
		switch l.op {
		case diffAdd:
			added++
		case diffDel:
			removed++
		}
	}
	return added, removed
}

// parseUnifiedDiff converts `git diff` output into diff lines,
// skipping file headers and rendering hunk headers as context
// markers.
func parseUnifiedDiff(text string) []diffLine {
	var out []diffLine
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "@@"):
			out = append(out, diffLine{op: diffCtx, text: line})
		case strings.HasPrefix(line, "+"):
			out = append(out, diffLine{op: diffAdd, text: line[1:]})
		case strings.HasPrefix(line, "-"):
			out = append(out, diffLine{op: diffDel, text: line[1:]})
		default:
			out = append(out, diffLine{op: diffCtx, text: line})
		}
	}
	return out
}

// renderDiffLinesHTML writes a colored table for pre-parsed diff
// lines.
func renderDiffLinesHTML(b *strings.Builder, lines []diffLine) {
	added, removed := diffStats(lines)
	fmt.Fprintf(b, "<table class=\"diff\" data-add=\"%d\" data-del=\"%d\">", added, removed)
	for _, l := range lines {
		cls := "ctx"
		marker := " "
		switch l.op {
		case diffDel:
			cls, marker = "del", "-"
		case diffAdd:
			cls, marker = "add", "+"
		}
		fmt.Fprintf(b, "<tr class=\"%s\"><td class=\"marker\">%s</td><td>%s</td></tr>", cls, marker, esc(l.text))
	}
	b.WriteString("</table>")
}

// renderDiffHTML writes a colored unified-diff table.
func renderDiffHTML(b *strings.Builder, oldText, newText string) {
	lines := lineDiff(oldText, newText)
	added, removed := diffStats(lines)
	fmt.Fprintf(b, "<table class=\"diff\" data-add=\"%d\" data-del=\"%d\">", added, removed)
	for _, l := range lines {
		cls := "ctx"
		marker := " "
		switch l.op {
		case diffDel:
			cls, marker = "del", "-"
		case diffAdd:
			cls, marker = "add", "+"
		}
		fmt.Fprintf(b, "<tr class=\"%s\"><td class=\"marker\">%s</td><td>%s</td></tr>", cls, marker, esc(l.text))
	}
	b.WriteString("</table>")
}

// hasDiffChange reports whether the diff contains any add/del line.
func hasDiffChange(oldText, newText string) bool {
	for _, l := range lineDiff(oldText, newText) {
		if l.op != diffCtx {
			return true
		}
	}
	return false
}
