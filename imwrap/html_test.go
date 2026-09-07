package imwrap

import (
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/stretchr/testify/require"
)

func testMessages() []proto.Message {
	user := proto.Message{
		ID: "m1", Role: proto.User, SessionID: "s1", CreatedAt: 1000,
		Parts: []proto.ContentPart{proto.TextContent{Text: "hello <script>alert(1)</script>"}},
	}
	assistant := proto.Message{
		ID: "m2", Role: proto.Assistant, SessionID: "s1", Model: "gpt-x", Provider: "openai", CreatedAt: 1001,
		Parts: []proto.ContentPart{
			proto.ReasoningContent{Thinking: "think **hard** about it"},
			proto.TextContent{Text: "doing it\n```go\nfmt.Println(42)\n```\n**bold** and *em* and `code` and [link](https://example.com)"},
			proto.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"ls -la"}`},
			proto.Finish{Reason: proto.FinishReasonEndTurn},
		},
	}
	tool := proto.Message{
		ID: "m3", Role: proto.Tool, SessionID: "s1", CreatedAt: 1002,
		Parts: []proto.ContentPart{proto.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "total 0\n-rw file & <tag>", IsError: false}},
	}
	return []proto.Message{user, assistant, tool}
}

func TestRenderTurnHTMLEscapesAndIncludesParts(t *testing.T) {
	t.Parallel()

	rc := proto.RunComplete{SessionID: "s1", RunID: "r1", Text: "all done"}
	html := string(RenderTurnHTML("fix the <bug>", testMessages(), &rc))

	require.Contains(t, html, "fix the &lt;bug&gt;")
	require.NotContains(t, html, "<script>alert(1)</script>")
	require.Contains(t, html, "&lt;script&gt;")
	require.Contains(t, html, "💭 Thinking")
	require.Contains(t, html, "think **hard** about it")
	require.Contains(t, html, "<code>code</code>")
	require.Contains(t, html, `<a href="https://example.com"`)
	require.Contains(t, html, "🔧 bash")
	require.Contains(t, html, "&#34;command&#34;: &#34;ls -la&#34;")
	require.Contains(t, html, "📤 bash")
	require.Contains(t, html, "total 0\n-rw file &amp; &lt;tag&gt;")
	require.Contains(t, html, "finished: end_turn")
	require.Contains(t, html, "role-user")
	require.Contains(t, html, "role-assistant")
	require.Contains(t, html, "role-tool")
}

func TestRenderTurnHTMLInProgressAndError(t *testing.T) {
	t.Parallel()

	html := string(RenderTurnHTML("p", testMessages(), nil))
	require.Contains(t, html, "进行中")

	rc := proto.RunComplete{SessionID: "s1", Error: "boom", Cancelled: true}
	html = string(RenderTurnHTML("p", testMessages(), &rc))
	require.Contains(t, html, "run error: boom")
	require.Contains(t, html, "run cancelled")
}

func TestRenderSessionHTML(t *testing.T) {
	t.Parallel()

	sess := proto.Session{ID: "s1", Title: "my session", MessageCount: 3, Cost: 0.5, UpdatedAt: 1700000000}
	html := string(RenderSessionHTML(&sess, testMessages()))
	require.Contains(t, html, "my session")
	require.Contains(t, html, "3 messages")
	require.Contains(t, html, "📤 bash")

	html = string(RenderSessionHTML(nil, nil))
	require.Contains(t, html, "(no messages)")
}

func TestMarkdownishFencesAndSafety(t *testing.T) {
	t.Parallel()

	out := markdownish("a ```x=<b>``` b")
	require.Contains(t, out, "x=&lt;b&gt;")
	require.NotContains(t, out, "<b>")

	out = markdownish("```\ncode\n```\ntail")
	require.Contains(t, out, "<pre class=\"code\">code</pre>")
}

func TestTruncateAndShortID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", truncate("abc", 10))
	require.Contains(t, truncate("abcdefghij", 4), "truncated")
	require.Equal(t, "12345678", shortID("1234567890abcdef"))
	require.Equal(t, "short", shortID("short"))
}

func TestSortSessionsByRecency(t *testing.T) {
	t.Parallel()
	sessions := []proto.Session{
		{ID: "a", UpdatedAt: 1},
		{ID: "c", UpdatedAt: 3},
		{ID: "b", UpdatedAt: 3},
	}
	sortSessionsByRecency(sessions)
	require.Equal(t, "b", sessions[0].ID)
	require.Equal(t, "c", sessions[1].ID)
	require.Equal(t, "a", sessions[2].ID)
}

func TestTurnCollector(t *testing.T) {
	t.Parallel()
	c := newTurnCollector()
	c.setPrompt("hi")
	c.apply(proto.Message{ID: "a", Parts: []proto.ContentPart{proto.TextContent{Text: "v1"}}})
	c.apply(proto.Message{ID: "b"})
	c.apply(proto.Message{ID: "a", Parts: []proto.ContentPart{proto.TextContent{Text: "v2"}}})

	require.Equal(t, "hi", c.prompt)
	got := c.snapshot()
	require.Len(t, got, 2)
	require.Equal(t, "a", got[0].ID)
	require.Equal(t, "v2", got[0].Content().Text)

	c.apply(proto.Message{}) // no ID, ignored
	require.Len(t, c.snapshot(), 2)
}

func TestTurnSummary(t *testing.T) {
	t.Parallel()
	msgs := testMessages()
	s := turnSummary(msgs, proto.RunComplete{Text: "line1\nline2"})
	require.True(t, strings.HasPrefix(s, "✅"))
	require.Contains(t, s, "最后回复: line1")
	require.NotContains(t, s, "line2")
	require.Contains(t, s, "1 次工具调用")
}
