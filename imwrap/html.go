package imwrap

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/roberChen/crush-sdk/proto"
)

// maxResultDisplay caps the tool result body rendered into the HTML;
// longer results are truncated with a note.
const maxResultDisplay = 64 * 1024

// SubAgentTranscript carries the child-session messages behind one
// task/agent tool call, rendered nested under the call in the HTML
// report (input, the sub-agent's own tool calls, and its output).
type SubAgentTranscript struct {
	// SessionID is the child session's ID.
	SessionID string
	// Messages are the child session's messages in order.
	Messages []proto.Message
}

// turnRenderCtx carries per-render state; the zero value renders
// without sub-agent transcripts.
type turnRenderCtx struct {
	subs []SubAgentTranscript
}

// TurnHTMLOption customizes [RenderTurnHTML].
type TurnHTMLOption func(*turnRenderCtx)

// WithSubAgents attaches sub-agent transcripts to a turn render.
// They are consumed in order by the turn's task/agent tool calls.
func WithSubAgents(subs []SubAgentTranscript) TurnHTMLOption {
	return func(c *turnRenderCtx) { c.subs = subs }
}

// RenderTurnHTML renders one completed (or in-progress) agent turn to
// a self-contained HTML document: the user prompt plus every message
// streamed during the run (thinking, tool calls with diff views for
// edit/write/multiedit and nested transcripts for task/agent calls,
// tool results, assistant text, finish reason). rc may be nil for
// in-progress renders (question intercepts).
func RenderTurnHTML(prompt string, msgs []proto.Message, rc *proto.RunComplete, opts ...TurnHTMLOption) []byte {
	ctx := &turnRenderCtx{}
	for _, opt := range opts {
		opt(ctx)
	}
	var b strings.Builder
	b.WriteString(htmlPageStart())
	b.WriteString("<header class=\"doc-header\"><h1>Crush 回复</h1>")
	fmt.Fprintf(&b, "<p class=\"sub\">prompt: %s</p>", esc(truncate(prompt, 500)))
	if rc != nil {
		if rc.Error != "" {
			fmt.Fprintf(&b, "<p class=\"error\">run error: %s</p>", esc(rc.Error))
		}
		if rc.Cancelled {
			b.WriteString("<p class=\"warn\">run cancelled</p>")
		}
	} else {
		b.WriteString("<p class=\"warn\">进行中 (等待回答/尚未结束)</p>")
	}
	fmt.Fprintf(&b, "<p class=\"sub\">generated at %s</p></header>", esc(time.Now().Format("2006-01-02 15:04:05")))
	renderMessages(&b, msgs, ctx)
	b.WriteString(htmlPageEnd())
	return []byte(b.String())
}

// RenderSessionHTML renders a whole session conversation (used by the
// /export command) to a self-contained HTML document. sess may be nil
// when session metadata is unavailable.
func RenderSessionHTML(sess *proto.Session, msgs []proto.Message) []byte {
	var b strings.Builder
	b.WriteString(htmlPageStart())
	b.WriteString("<header class=\"doc-header\"><h1>Crush 会话导出</h1>")
	if sess != nil {
		fmt.Fprintf(&b, "<p class=\"sub\">%s</p>", esc(sessionSubtitle(sess)))
	}
	fmt.Fprintf(&b, "<p class=\"sub\">%d messages, generated at %s</p></header>",
		len(msgs), esc(time.Now().Format("2006-01-02 15:04:05")))
	renderMessages(&b, msgs, &turnRenderCtx{})
	b.WriteString(htmlPageEnd())
	return []byte(b.String())
}

func sessionSubtitle(sess *proto.Session) string {
	title := sess.Title
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("%s, id %s, updated %s, %.4f $, %d msgs",
		title, shortID(sess.ID), time.Unix(sess.UpdatedAt, 0).Format("2006-01-02 15:04:05"),
		sess.Cost, sess.MessageCount)
}

// renderMessages writes the message list body.
func renderMessages(b *strings.Builder, msgs []proto.Message, ctx *turnRenderCtx) {
	b.WriteString("<main>")
	if len(msgs) == 0 {
		b.WriteString("<p class=\"sub\">(no messages)</p>")
	}
	for i := range msgs {
		renderMessage(b, &msgs[i], ctx)
	}
	b.WriteString("</main>")
}

func renderMessage(b *strings.Builder, m *proto.Message, ctx *turnRenderCtx) {
	fmt.Fprintf(b, "<section class=\"msg role-%s\">", esc(string(m.Role)))

	fmt.Fprintf(b, "<div class=\"msg-head\"><span class=\"badge\">%s</span>", esc(string(m.Role)))
	if m.Role == proto.Assistant && (m.Model != "" || m.Provider != "") {
		fmt.Fprintf(b, "<span class=\"meta\">%s / %s</span>", esc(m.Provider), esc(m.Model))
	}
	if m.CreatedAt > 0 {
		fmt.Fprintf(b, "<span class=\"meta\">%s</span>", esc(time.Unix(m.CreatedAt, 0).Format("15:04:05")))
	}
	if m.IsSummaryMessage {
		b.WriteString("<span class=\"meta\">summary</span>")
	}
	b.WriteString("</div>")

	for _, part := range m.Parts {
		switch p := part.(type) {
		case proto.ReasoningContent:
			if p.Thinking == "" && p.Signature == "" {
				continue
			}
			fmt.Fprintf(b, "<details class=\"thinking\"><summary>💭 Thinking</summary><pre>%s</pre></details>", esc(p.Thinking))
		case proto.TextContent:
			if p.Text == "" {
				continue
			}
			fmt.Fprintf(b, "<div class=\"text\">%s</div>", markdownish(p.Text))
		case proto.ToolCall:
			renderToolCall(b, p, ctx)
		case proto.ToolResult:
			cls := "toolresult"
			summary := "📤 " + p.Name
			if p.IsError {
				cls += " failed"
				summary += " (error)"
			}
			fmt.Fprintf(b, "<details class=\"%s\"><summary>%s</summary><pre class=\"code\">%s</pre></details>", cls, esc(summary), esc(truncate(p.Content, maxResultDisplay)))
		case proto.Finish:
			note := "finished: " + string(p.Reason)
			if p.Message != "" {
				note += " (" + p.Message + ")"
			}
			cls := "finish"
			if p.Reason == proto.FinishReasonError || p.Reason == proto.FinishReasonCanceled {
				cls += " failed"
			}
			fmt.Fprintf(b, "<div class=\"%s\">%s</div>", cls, esc(note))
		case proto.ImageURLContent:
			fmt.Fprintf(b, "<div class=\"text\">image: <a href=\"%s\" target=\"_blank\" rel=\"noreferrer\">%s</a></div>", esc(p.URL), esc(truncate(p.URL, 120)))
		case proto.ShellCommand:
			fmt.Fprintf(b, "<details class=\"toolcall\"><summary>⌨ %s (exit %d)</summary><pre class=\"code\">%s</pre></details>", esc(p.Command), p.ExitCode, esc(p.Output))
		}
	}
	b.WriteString("</section>")
}

// Tool-name groups for specialized rendering. The edit-family names
// and sub-agent names mirror the server's tool registry.
var (
	editToolNames     = map[string]bool{"edit": true, "write": true}
	subAgentToolNames = map[string]bool{"task": true, "agent": true}
)

// renderToolCall renders a tool call with the best view for its
// family: colored diffs for edit/write/multiedit, input plus a
// nested sub-agent transcript for task/agent, and re-indented JSON
// for everything else.
func renderToolCall(b *strings.Builder, p proto.ToolCall, ctx *turnRenderCtx) {
	input := parseToolInput(p.Input)
	switch {
	case editToolNames[p.Name]:
		renderEditToolCall(b, p.Name, input)
	case p.Name == "multiedit":
		renderMultiEditToolCall(b, input)
	case subAgentToolNames[p.Name]:
		renderSubAgentToolCall(b, p, ctx)
	default:
		fmt.Fprintf(b, "<details class=\"toolcall\"><summary>🔧 %s</summary><pre class=\"code\">%s</pre></details>", esc(p.Name), esc(prettyJSON(p.Input)))
	}
}

// parseToolInput decodes a tool call's raw JSON input leniently.
func parseToolInput(raw string) map[string]any {
	out := make(map[string]any)
	if raw == "" {
		return out
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return out
	}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	return m
}

func inputString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// renderEditToolCall renders edit/write as a colored diff between
// old and new content.
func renderEditToolCall(b *strings.Builder, name string, input map[string]any) {
	filePath := inputString(input, "file_path", "filePath", "path")
	oldText := inputString(input, "old_string", "old_content")
	newText := inputString(input, "new_string", "new_content")

	kind := ""
	switch {
	case oldText == "" && newText != "":
		kind = "（新建文件）"
	case oldText != "" && newText == "":
		kind = "（清空文件）"
	}
	fmt.Fprintf(b, "<details class=\"toolcall editdiff\"><summary>✏️ %s %s%s</summary>",
		esc(name), esc(filePath), esc(kind))
	renderDiffHTML(b, oldText, newText)
	b.WriteString("</details>")
}

// renderMultiEditToolCall renders each edit of a multiedit call as
// its own diff hunk.
func renderMultiEditToolCall(b *strings.Builder, input map[string]any) {
	filePath := inputString(input, "file_path", "filePath", "path")
	rawEdits, _ := input["edits"].([]any)

	fmt.Fprintf(b, "<details class=\"toolcall editdiff\"><summary>✏️ multiedit %s（%d 处修改）</summary>",
		esc(filePath), len(rawEdits))
	for i, re := range rawEdits {
		edit, ok := re.(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(b, "<div class=\"hunk-head\">修改 %d</div>", i+1)
		renderDiffHTML(b,
			inputString(edit, "old_string", "old_content"),
			inputString(edit, "new_string", "new_content"))
	}
	b.WriteString("</details>")
}

// renderSubAgentToolCall renders a task/agent call: the input given
// to the sub-agent, followed by the sub-agent session's own messages
// (its tool calls and output) when a transcript is available. The
// call's final output also appears in the regular tool-result part.
func renderSubAgentToolCall(b *strings.Builder, p proto.ToolCall, ctx *turnRenderCtx) {
	input := parseToolInput(p.Input)
	description := inputString(input, "description")
	prompt := inputString(input, "prompt", "input")

	summary := "🤖 " + p.Name
	if description != "" {
		summary += ": " + truncate(description, 80)
	} else if prompt != "" {
		summary += ": " + truncate(prompt, 80)
	}
	fmt.Fprintf(b, "<details class=\"toolcall subagent\"><summary>%s</summary>", esc(summary))

	fmt.Fprintf(b, "<div class=\"hunk-head\">输入</div><pre class=\"code\">%s</pre>", esc(prompt))
	var sub *SubAgentTranscript
	if ctx != nil && len(ctx.subs) > 0 {
		s := ctx.subs[0]
		ctx.subs = ctx.subs[1:]
		sub = &s
	}
	if sub != nil {
		fmt.Fprintf(b, "<details class=\"subagent-session\" open><summary>🧒 子 agent 会话 %s（%d 条消息）</summary>",
			esc(shortID(sub.SessionID)), len(sub.Messages))
		renderMessages(b, sub.Messages, &turnRenderCtx{})
		b.WriteString("</details>")
	} else {
		b.WriteString("<p class=\"sub\">（子会话消息不可用）</p>")
	}
	b.WriteString("</details>")
}

// turnHasSubAgentCall reports whether any message in the turn made a
// task/agent tool call.
func turnHasSubAgentCall(msgs []proto.Message) bool {
	for i := range msgs {
		for _, tc := range msgs[i].ToolCalls() {
			if subAgentToolNames[tc.Name] {
				return true
			}
		}
	}
	return false
}

// firstTurnCreatedAt returns the earliest message timestamp in the
// turn (unix seconds), or 0 when unknown.
func firstTurnCreatedAt(msgs []proto.Message) int64 {
	var first int64
	for i := range msgs {
		if msgs[i].CreatedAt > 0 && (first == 0 || msgs[i].CreatedAt < first) {
			first = msgs[i].CreatedAt
		}
	}
	return first
}

// markdownish renders a small, safe subset of markdown: fenced code
// blocks, inline code, bold, italics, and http(s) links. The input is
// HTML-escaped before any markup is applied, so raw HTML never passes
// through.
func markdownish(s string) string {
	chunks := strings.Split(s, "```")
	var b strings.Builder
	for i, chunk := range chunks {
		if i%2 == 1 {
			fmt.Fprintf(&b, "<pre class=\"code\">%s</pre>", esc(trimEdgeNewlines(chunk)))
			continue
		}
		if i > 0 {
			// Drop the newline that immediately follows a closing fence
			// so it does not become a spurious blank line.
			chunk = strings.TrimPrefix(chunk, "\n")
		}
		b.WriteString(inlineMarkdown(chunk))
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
	reInlineCode = regexp.MustCompile("`([^`\n]+)`")
	reBold       = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reEmph       = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
)

func inlineMarkdown(s string) string {
	s = esc(s)
	s = reLink.ReplaceAllString(s, "<a href=\"$2\" target=\"_blank\" rel=\"noreferrer\">$1</a>")
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reEmph.ReplaceAllString(s, "$1<em>$2</em>")
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

// prettyJSON re-indents a JSON tool-call input; non-JSON input is
// returned unchanged.
func prettyJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
}

func esc(s string) string {
	return html.EscapeString(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (%d bytes truncated)", len(s)-n)
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// timestampSlug builds a filesystem-safe timestamp for file names.
func timestampSlug(t time.Time) string {
	return t.Format("20060102-150405")
}

// sortSessionsByRecency orders sessions most-recently-updated first,
// the order /sessions lists them in.
func sortSessionsByRecency(sess []proto.Session) {
	sort.SliceStable(sess, func(i, j int) bool {
		if sess[i].UpdatedAt != sess[j].UpdatedAt {
			return sess[i].UpdatedAt > sess[j].UpdatedAt
		}
		return sess[i].ID < sess[j].ID
	})
}

func htmlPageStart() string {
	return "<!DOCTYPE html><html lang=\"zh\"><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
		"<title>Crush</title><style>" + htmlCSS + "</style></head><body>"
}

func htmlPageEnd() string {
	return "</body></html>"
}

const htmlCSS = `
:root { color-scheme: dark; }
* { box-sizing: border-box; }
body { margin: 0; padding: 16px; background: #0f1116; color: #d7dce4;
  font: 14px/1.6 -apple-system, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif; }
.doc-header { border-bottom: 1px solid #262b36; padding-bottom: 10px; margin-bottom: 14px; }
.doc-header h1 { font-size: 18px; margin: 0 0 6px; }
.sub { color: #8b93a3; font-size: 12px; margin: 2px 0; }
.error { color: #ff8080; font-size: 13px; }
.warn { color: #ffc46b; font-size: 13px; }
main { max-width: 900px; }
.msg { border: 1px solid #262b36; border-radius: 8px; padding: 10px 12px; margin: 10px 0; background: #151922; }
.role-user { border-left: 3px solid #4f9cf9; }
.role-assistant { border-left: 3px solid #43d9a3; }
.role-tool { border-left: 3px solid #8b93a3; background: #12151d; }
.role-system { border-left: 3px solid #c9a227; }
.msg-head { display: flex; gap: 10px; align-items: baseline; margin-bottom: 6px; }
.badge { font-weight: 600; text-transform: uppercase; font-size: 11px; letter-spacing: .05em; }
.meta { color: #8b93a3; font-size: 11px; }
.text { white-space: normal; word-break: break-word; }
.text a { color: #6fb3ff; }
pre.code, .thinking pre { background: #0b0d12; border: 1px solid #262b36; border-radius: 6px;
  padding: 8px 10px; overflow-x: auto; font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  white-space: pre-wrap; word-break: break-word; color: #c7d0dd; }
details { margin: 6px 0; }
details summary { cursor: pointer; color: #9fb0c8; font-size: 12.5px; user-select: none; }
.thinking summary { color: #b39ddb; }
.toolcall summary { color: #6fb3ff; }
.toolresult summary { color: #7fd49b; }
.toolresult.failed summary, .finish.failed { color: #ff8080; }
.finish { color: #8b93a3; font-size: 11.5px; margin-top: 6px; }
code { background: #0b0d12; border: 1px solid #262b36; border-radius: 4px; padding: 0 4px;
  font: 12px ui-monospace, Menlo, Consolas, monospace; }
table.diff { border-collapse: collapse; width: 100%; margin: 6px 0;
  font: 12px/1.5 ui-monospace, Menlo, Consolas, monospace; }
table.diff td { padding: 0 8px; white-space: pre-wrap; word-break: break-word; vertical-align: top; }
table.diff td.marker { width: 1ch; text-align: center; color: #5b6472; user-select: none;
  border-right: 1px solid #262b36; }
table.diff tr.ctx td { color: #8b93a3; background: #0e1118; }
table.diff tr.del td { background: #2a1418; color: #ff9daa; }
table.diff tr.add td { background: #122a19; color: #8ee2a9; }
.hunk-head { color: #9fb0c8; font-size: 11.5px; margin: 8px 0 2px; }
.subagent-session { border: 1px dashed #33405a; border-radius: 6px; padding: 6px 10px; margin: 6px 0; }
.subagent-session > summary { color: #b39ddb; }
`
