package imwrap

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// diffToggleCounter mints unique checkbox ids so multiple diff blocks
// in one document toggle independently.
var diffToggleCounter atomic.Int64

// maxDiffLines caps the LCS matrix; larger inputs fall back to plain
// before/after blocks.
const maxDiffLines = 2000

type diffOp int

const (
	diffCtx diffOp = iota
	diffDel
	diffAdd
	// hunkMarker is a pseudo-op for "@@ ... @@" hunk headers (and the
	// "\ No newline" marker) so renderers style them distinctly.
	hunkMarker diffOp = 99
)

type diffLine struct {
	op   diffOp
	text string
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

// hasDiffChange reports whether the diff contains any add/del line.
func hasDiffChange(oldText, newText string) bool {
	for _, l := range lineDiff(oldText, newText) {
		if l.op == diffDel || l.op == diffAdd {
			return true
		}
	}
	return false
}

// splitRow is one row of a side-by-side diff: paired old/new cells.
type splitRow struct {
	left, right     string
	leftOp, rightOp diffOp
	leftHl, rightHl string // pre-rendered HTML with intra-line highlight
}

// buildSplitRows pairs consecutive remove/add runs row by row (extra
// lines get an empty counterpart) and pre-renders intra-line
// highlights for each pair. Hunk markers collapse into the left
// gutter only.
func buildSplitRows(lines []diffLine) []splitRow {
	var rows []splitRow
	i := 0
	for i < len(lines) {
		switch lines[i].op {
		case hunkMarker:
			rows = append(rows, splitRow{left: lines[i].text, leftOp: hunkMarker, leftHl: esc(lines[i].text)})
			i++
		case diffCtx:
			rows = append(rows, splitRow{
				left: lines[i].text, leftOp: diffCtx,
				right: lines[i].text, rightOp: diffCtx,
				leftHl: esc(lines[i].text), rightHl: esc(lines[i].text),
			})
			i++
		case diffDel, diffAdd:
			var dels, adds []diffLine
			for i < len(lines) && (lines[i].op == diffDel || lines[i].op == diffAdd) {
				if lines[i].op == diffDel {
					dels = append(dels, lines[i])
				} else {
					adds = append(adds, lines[i])
				}
				i++
			}
			for k := 0; k < len(dels) || k < len(adds); k++ {
				row := splitRow{leftOp: diffCtx, rightOp: diffCtx}
				if k < len(dels) {
					row.left, row.leftOp = dels[k].text, diffDel
				}
				if k < len(adds) {
					row.right, row.rightOp = adds[k].text, diffAdd
				}
				if k < len(dels) && k < len(adds) {
					row.leftHl, row.rightHl = highlightIntraLine(row.left, row.right)
				} else {
					row.leftHl, row.rightHl = esc(row.left), esc(row.right)
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// highlightIntraLine renders two paired lines with the differing
// middle segment wrapped in <span class="hl">: the common prefix and
// suffix stay plain so readers see exactly which characters changed.
func highlightIntraLine(oldLine, newLine string) (oldHTML, newHTML string) {
	if oldLine == newLine {
		return esc(oldLine), esc(newLine)
	}
	p := commonPrefixLen(oldLine, newLine)
	s := commonSuffixLen(oldLine[p:], newLine[p:])
	oldMid := oldLine[p : len(oldLine)-s]
	newMid := newLine[p : len(newLine)-s]
	if oldMid == "" && newMid == "" {
		return esc(oldLine), esc(newLine)
	}
	var ob, nb strings.Builder
	fmt.Fprintf(&ob, "%s<span class=\"hl\">%s</span>%s",
		esc(oldLine[:p]), esc(oldMid), esc(oldLine[len(oldLine)-s:]))
	fmt.Fprintf(&nb, "%s<span class=\"hl\">%s</span>%s",
		esc(newLine[:p]), esc(newMid), esc(newLine[len(newLine)-s:]))
	return ob.String(), nb.String()
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// renderDiffHTML writes a complete diff block (stats badge plus
// unified and side-by-side views behind a CSS-only toggle) between
// old and new.
func renderDiffHTML(b *strings.Builder, oldText, newText string) {
	renderDiffLinesHTML(b, lineDiff(oldText, newText))
}

// renderDiffLinesHTML writes the complete diff block for pre-parsed
// lines: a visible +/- stats badge, the unified table (default), and
// a side-by-side table revealed by the checkbox toggle (pure CSS, no
// script, so it survives IM previews that strip scripts). Intra-line
// highlights mark the changed characters inside paired rows.
func renderDiffLinesHTML(b *strings.Builder, lines []diffLine) {
	added, removed := diffStats(lines)
	rows := buildSplitRows(lines)

	toggleID := fmt.Sprintf("diff-%d", diffToggleCounter.Add(1))
	b.WriteString("<div class=\"diffwrap\">")
	// The checkbox must be a preceding sibling of the tables for the
	// CSS-only toggle to work; the visible label references it by id.
	fmt.Fprintf(b, "<input type=\"checkbox\" id=\"%s\" class=\"diff-toggle\">", toggleID)
	fmt.Fprintf(b, "<div class=\"diff-toolbar\"><span class=\"stat add\">+%d</span><span class=\"stat del\">-%d</span>",
		added, removed)
	fmt.Fprintf(b, "<label class=\"diff-switch\" for=\"%s\">并排对比</label></div>", toggleID)

	// Unified view (default).
	fmt.Fprintf(b, "<table class=\"diff unified\" data-add=\"%d\" data-del=\"%d\">", added, removed)
	ln := 0
	for _, l := range lines {
		cls, marker := "ctx", " "
		switch l.op {
		case diffDel:
			cls, marker = "del", "-"
		case diffAdd:
			cls, marker = "add", "+"
		case hunkMarker:
			cls = "hunk"
		}
		if l.op == diffDel || l.op == diffAdd {
			ln++
		}
		fmt.Fprintf(b, "<tr class=\"%s\"><td class=\"marker\">%s</td><td class=\"ln\">%d</td><td>%s</td></tr>",
			cls, marker, ln, esc(l.text))
	}
	b.WriteString("</table>")

	// Side-by-side view (shown when the checkbox is ticked).
	b.WriteString("<table class=\"diff split\">")
	for _, r := range rows {
		if r.leftOp == hunkMarker {
			fmt.Fprintf(b, "<tr class=\"hunk\"><td class=\"marker\"></td><td colspan=\"3\">%s</td></tr>", r.leftHl)
			continue
		}
		clsL, clsR := opClass(r.leftOp), opClass(r.rightOp)
		fmt.Fprintf(b, "<tr><td class=\"%s\">%s</td><td class=\"%s\">%s</td><td class=\"%s\">%s</td><td class=\"%s\">%s</td></tr>",
			clsL, r.leftHl, clsL, esc(r.left), clsR, r.rightHl, clsR, esc(r.right))
	}
	b.WriteString("</table>")

	b.WriteString("</div>")
}

func opClass(op diffOp) string {
	switch op {
	case diffDel:
		return "del"
	case diffAdd:
		return "add"
	default:
		return "ctx"
	}
}

// unifiedFile is one file's section of a `git diff` output.
type unifiedFile struct {
	// Header is the "diff --git a/x b/x" line plus follow-up metadata
	// (modes, renames, index).
	Header string
	// Lines are the parsed diff lines for this file.
	Lines []diffLine
}

// parseUnifiedDiffFiles splits `git diff` output into per-file
// sections so each renders under its own header instead of blurring
// multi-file diffs together.
func parseUnifiedDiffFiles(text string) []unifiedFile {
	var files []unifiedFile
	var cur *unifiedFile
	text = strings.TrimSuffix(text, "\n")
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, unifiedFile{Header: line})
			cur = &files[len(files)-1]
		case cur == nil:
			continue
		case strings.HasPrefix(line, "index "), strings.HasPrefix(line, "old mode"),
			strings.HasPrefix(line, "new mode"), strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "rename "), strings.HasPrefix(line, "copy "),
			strings.HasPrefix(line, "deleted file"), strings.HasPrefix(line, "new file"):
			cur.Header += "\n" + line
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "\\ No newline"),
			strings.HasPrefix(line, "Binary files "):
			cur.Lines = append(cur.Lines, diffLine{op: hunkMarker, text: line})
		case strings.HasPrefix(line, "+"):
			cur.Lines = append(cur.Lines, diffLine{op: diffAdd, text: line[1:]})
		case strings.HasPrefix(line, "-"):
			cur.Lines = append(cur.Lines, diffLine{op: diffDel, text: line[1:]})
		default:
			cur.Lines = append(cur.Lines, diffLine{op: diffCtx, text: line})
		}
	}
	return files
}

// renderUnifiedFileHTML writes one git-diff file section: a header
// line naming the file (and its metadata), then the diff block.
func renderUnifiedFileHTML(b *strings.Builder, f unifiedFile) {
	if strings.TrimSpace(f.Header) == "" {
		return
	}
	fmt.Fprintf(b, "<div class=\"diff-file\">%s</div>", strings.ReplaceAll(esc(f.Header), "\n", "<br>"))
	if len(f.Lines) == 0 {
		b.WriteString("<p class=\"sub\">（无文本 diff：二进制或纯重命名变更）</p>")
		return
	}
	renderDiffLinesHTML(b, f.Lines)
}

// parseUnifiedDiff converts `git diff` output into flat diff lines
// with file headers retained as hunk markers, for callers that do not
// need per-file grouping.
func parseUnifiedDiff(text string) []diffLine {
	var out []diffLine
	for _, f := range parseUnifiedDiffFiles(text) {
		out = append(out, diffLine{op: hunkMarker, text: f.Header})
		out = append(out, f.Lines...)
	}
	return out
}
