package imwrap

import (
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/stretchr/testify/require"
)

func TestLineDiff(t *testing.T) {
	t.Parallel()

	lines := lineDiff("a\nb\nc\n", "a\nx\nc\nd\n")
	var ops []diffOp
	for _, l := range lines {
		ops = append(ops, l.op)
	}
	require.Equal(t, []diffOp{diffCtx, diffDel, diffAdd, diffCtx, diffAdd}, ops)
	added, removed := diffStats(lines)
	require.Equal(t, 2, added)
	require.Equal(t, 1, removed)

	// Empty old means a fresh file: everything added.
	lines = lineDiff("", "one\ntwo\n")
	added, removed = diffStats(lines)
	require.Equal(t, 2, added)
	require.Equal(t, 0, removed)

	// Identical content: no changes.
	require.False(t, hasDiffChange("same\n", "same\n"))
	require.True(t, hasDiffChange("same\n", "other\n"))

	// Huge inputs fall back to block replacement without hanging.
	var bigOld, bigNew strings.Builder
	for i := range 3000 {
		bigOld.WriteString("old\n")
		if i%2 == 0 {
			bigNew.WriteString("new\n")
		}
	}
	lines = lineDiff(bigOld.String(), bigNew.String())
	added, removed = diffStats(lines)
	require.Equal(t, 1500, added)
	require.Equal(t, 3000, removed)
}

func editTurnMessages() []proto.Message {
	return []proto.Message{
		{
			ID: "a1", Role: proto.Assistant, SessionID: "s1",
			Parts: []proto.ContentPart{
				proto.ToolCall{
					ID:    "tc1",
					Name:  "edit",
					Input: `{"file_path":"/src/app.go","old_string":"func old() {}\n","new_string":"func new() {}\n// fresh\n"}`,
				},
				proto.ToolCall{
					ID:   "tc2",
					Name: "multiedit",
					Input: `{"file_path":"/src/util.go","edits":[` +
						`{"old_string":"a\n","new_string":"b\n"},` +
						`{"old_string":"c\n","new_string":""}]}`,
				},
				proto.ToolCall{
					ID:    "tc3",
					Name:  "bash",
					Input: `{"command":"go build ./..."}`,
				},
			},
		},
		{
			ID: "t1", Role: proto.Tool, SessionID: "s1",
			Parts: []proto.ContentPart{proto.ToolResult{ToolCallID: "tc1", Name: "edit", Content: "ok"}},
		},
	}
}

func TestRenderEditToolCallsAsDiffs(t *testing.T) {
	t.Parallel()

	html := string(RenderTurnHTML("edit things", editTurnMessages(), nil))

	// edit renders a diff table with the changed lines colored.
	require.Contains(t, html, "✏️ edit /src/app.go")
	require.Contains(t, html, "func new() {}")
	require.Contains(t, html, "<tr class=\"del\">")
	require.Contains(t, html, "<tr class=\"add\">")

	// multiedit renders one hunk per edit.
	require.Contains(t, html, "✏️ multiedit /src/util.go（2 处修改）")
	require.Contains(t, html, "修改 1")
	require.Contains(t, html, "修改 2")

	// Unrelated tools keep the JSON view.
	require.Contains(t, html, "🔧 bash")
	require.Contains(t, html, "go build ./...")

	// No sub-agent transcripts were consumed.
	require.Contains(t, html, "/src/app.go")
}

func TestRenderSubAgentToolCallWithTranscript(t *testing.T) {
	t.Parallel()

	msgs := []proto.Message{
		{
			ID: "a1", Role: proto.Assistant, SessionID: "s1", CreatedAt: 100,
			Parts: []proto.ContentPart{
				proto.ToolCall{
					ID:    "tc1",
					Name:  "task",
					Input: `{"description":"scan the repo","prompt":"scan everything and report"}`,
				},
			},
		},
		{
			ID: "t1", Role: proto.Tool, SessionID: "s1",
			Parts: []proto.ContentPart{proto.ToolResult{ToolCallID: "tc1", Name: "task", Content: "scan finished: 3 issues"}},
		},
	}
	subs := []SubAgentTranscript{
		{
			SessionID: "child-1",
			Messages: []proto.Message{
				{ID: "c-u", Role: proto.User, Parts: []proto.ContentPart{proto.TextContent{Text: "scan everything and report"}}},
				{ID: "c-a", Role: proto.Assistant, Parts: []proto.ContentPart{
					proto.ToolCall{ID: "c-tc", Name: "bash", Input: `{"command":"grep -r TODO"}`},
				}},
				{ID: "c-t", Role: proto.Tool, Parts: []proto.ContentPart{proto.ToolResult{ToolCallID: "c-tc", Name: "bash", Content: "todo1"}}},
				{ID: "c-f", Role: proto.Assistant, Parts: []proto.ContentPart{proto.TextContent{Text: "sub agent final words"}}},
			},
		},
	}

	html := string(RenderTurnHTML("spawn a helper", msgs, nil, WithSubAgents(subs)))

	// Input is shown.
	require.Contains(t, html, "🤖 task: scan the repo")
	require.Contains(t, html, "输入")
	require.Contains(t, html, "scan everything and report")

	// The nested transcript shows the sub-agent's own tool call and
	// output.
	require.Contains(t, html, "🧒 子 agent 会话 child-1（4 条消息）")
	require.Contains(t, html, "🔧 bash")
	require.Contains(t, html, "grep -r TODO")
	require.Contains(t, html, "todo1")
	require.Contains(t, html, "sub agent final words")

	// The outer result (输出) is rendered by the tool-result part.
	require.Contains(t, html, "📤 task")
	require.Contains(t, html, "scan finished: 3 issues")
}

func TestRenderSubAgentToolCallWithoutTranscript(t *testing.T) {
	t.Parallel()

	msgs := []proto.Message{
		{
			ID: "a1", Role: proto.Assistant, SessionID: "s1",
			Parts: []proto.ContentPart{proto.ToolCall{ID: "tc1", Name: "agent", Input: `{"prompt":"do it"}`}},
		},
	}
	html := string(RenderTurnHTML("p", msgs, nil))
	require.Contains(t, html, "🤖 agent: do it")
	require.Contains(t, html, "（子会话消息不可用）")
}

func TestTurnHasSubAgentCallAndFirstCreatedAt(t *testing.T) {
	t.Parallel()

	plain := editTurnMessages()
	require.False(t, turnHasSubAgentCall(plain))
	withTask := append(plain, proto.Message{
		ID: "a2", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.ToolCall{ID: "tc9", Name: "task", Input: `{}`}},
	})
	require.True(t, turnHasSubAgentCall(withTask))

	require.Zero(t, firstTurnCreatedAt(plain))
	timed := []proto.Message{
		{ID: "x", CreatedAt: 200},
		{ID: "y", CreatedAt: 150},
	}
	require.EqualValues(t, 150, firstTurnCreatedAt(timed))
}
