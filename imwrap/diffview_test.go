package imwrap

import (
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/stretchr/testify/require"
)

func TestSplitRowPairing(t *testing.T) {
	t.Parallel()

	// 2 dels vs 1 add: extra del pairs with an empty right cell.
	lines := lineDiff("a\nb\nc\n", "a\nx\n")
	rows := buildSplitRows(lines)

	require.GreaterOrEqual(t, len(rows), 3)
	var delCells, addCells, emptyRight int
	for _, r := range rows {
		if r.leftOp == diffDel {
			delCells++
		}
		if r.rightOp == diffAdd {
			addCells++
		}
		if r.leftOp == diffDel && r.right == "" {
			emptyRight++
		}
	}
	require.Equal(t, 2, delCells)
	require.Equal(t, 1, addCells)
	require.Equal(t, 1, emptyRight)
}

func TestIntraLineHighlight(t *testing.T) {
	t.Parallel()

	oldHTML, newHTML := highlightIntraLine("return x + 1\n", "return x + 2\n")
	require.Contains(t, oldHTML, `>1</span>`)
	require.Contains(t, newHTML, `>2</span>`)
	// Common prefix and suffix stay plain text.
	require.Contains(t, oldHTML, "return x + ")
	require.NotContains(t, oldHTML, "<span class=\"hl\">return")

	// Identical lines get no highlight.
	o, n := highlightIntraLine("same\n", "same\n")
	require.Equal(t, "same\n", o)
	require.Equal(t, "same\n", n)
}

func TestDiffBlockRendersBothViews(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	renderDiffHTML(&b, "func old() {}\n", "func new() {}\n")
	html := b.String()

	// Unified view with line numbers and markers.
	require.Contains(t, html, `class="diff unified"`)
	require.Contains(t, html, `class="del"`)
	require.Contains(t, html, `class="add"`)
	require.Contains(t, html, `class="ln"`)

	// Side-by-side view with paired cells and intra-line highlight.
	require.Contains(t, html, `class="diff split"`)
	require.Contains(t, html, `class="hl"`)

	// Visible stats badge and CSS-only toggle (checkbox precedes the
	// tables as a sibling; label references it).
	require.Contains(t, html, `class="stat add">+1`)
	require.Contains(t, html, `class="stat del">-1`)
	require.Contains(t, html, `class="diff-toggle"`)
	require.Contains(t, html, `for="diff-`)
	require.Contains(t, html, "并排对比")
	// The checkbox must appear before the tables for the sibling
	// selector to work.
	toggle := strings.Index(html, `class="diff-toggle"`)
	unified := strings.Index(html, `class="diff unified"`)
	split := strings.Index(html, `class="diff split"`)
	require.Less(t, toggle, unified)
	require.Less(t, toggle, split)
}

func TestParseUnifiedDiffFilesSeparatesFiles(t *testing.T) {
	t.Parallel()

	gitDiff := `diff --git a/one.go b/one.go
index 111..222 100644
--- a/one.go
+++ b/one.go
@@ -1,2 +1,2 @@
 ctx
-old line
+new line
diff --git a/two.md b/two.md
new file mode 100644
index 000..333
--- /dev/null
+++ b/two.md
@@ -0,0 +1,1 @@
+hello two
`
	files := parseUnifiedDiffFiles(gitDiff)
	require.Len(t, files, 2)
	require.Contains(t, files[0].Header, "a/one.go b/one.go")
	require.Contains(t, files[0].Header, "index 111..222") // metadata folded into header
	require.Len(t, files[0].Lines, 4)                      // hunk header + ctx + del + add
	require.Contains(t, files[1].Header, "new file mode")

	var b strings.Builder
	renderUnifiedFileHTML(&b, files[0])
	require.Contains(t, b.String(), "a/one.go b/one.go")
	require.Contains(t, b.String(), "old line")
	require.Contains(t, b.String(), "@@ -1,2 +1,2 @@")
}

func TestParseUnifiedDiffFilesBinaryNoLines(t *testing.T) {
	t.Parallel()

	files := parseUnifiedDiffFiles("diff --git a/img.png b/img.png\nindex 1..2 Binary files differ\nBinary files a/img.png and b/img.png differ\n")
	require.Len(t, files, 1)
	// The binary note renders as a styled marker row, not a fake
	// context line.
	require.Len(t, files[0].Lines, 1)
	require.Equal(t, hunkMarker, files[0].Lines[0].op)
	var b strings.Builder
	renderUnifiedFileHTML(&b, files[0])
	require.Contains(t, b.String(), "Binary files a/img.png and b/img.png differ")
}

func TestEditToolDiffIncludesToggleAndHighlights(t *testing.T) {
	t.Parallel()

	html := string(RenderTurnHTML("p", []proto.Message{{
		ID: "a1", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.ToolCall{
			ID:    "tc1",
			Name:  "edit",
			Input: `{"file_path":"/x.go","old_string":"value = compute(input)\n","new_string":"value = computeAll(input)\n"}`,
		}},
	}}, nil))

	require.Contains(t, html, "✏️ edit /x.go")
	require.Contains(t, html, "diff-toggle")
	require.Contains(t, html, `class="diff split"`)
	require.Contains(t, html, `class="hl"`)
	require.Contains(t, html, "compute")
}
