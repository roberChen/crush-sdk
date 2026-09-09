package imwrap

import (
	"encoding/json"
	"fmt"
	"html"
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

// turnRenderCtx carries per-render state. The zero value renders
// without sub-agent transcripts; renderMessages fills the result
// index so each tool call renders next to its matching result.
type turnRenderCtx struct {
	subs    []SubAgentTranscript
	results map[string]proto.ToolResult
	shown   map[string]bool // toolCallID -> result rendered with its call
	footer  *SessionFooter
}

// TurnHTMLOption customizes [RenderTurnHTML].
type TurnHTMLOption func(*turnRenderCtx)

// WithSubAgents attaches sub-agent transcripts to a turn render.
// They are consumed in order by the turn's task/agent tool calls.
func WithSubAgents(subs []SubAgentTranscript) TurnHTMLOption {
	return func(c *turnRenderCtx) { c.subs = subs }
}

// WithFooter appends the session status footer (title, directory,
// model, context usage, git branch) to the rendered document.
func WithFooter(f *SessionFooter) TurnHTMLOption {
	return func(c *turnRenderCtx) { c.footer = f }
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
	renderFooterHTML(&b, ctx.footer)
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
	if ctx.results == nil {
		ctx.results = indexToolResults(msgs)
		ctx.shown = make(map[string]bool)
	}
	b.WriteString("<main>")
	if len(msgs) == 0 {
		b.WriteString("<p class=\"sub\">(no messages)</p>")
	}
	for i := range msgs {
		renderMessage(b, &msgs[i], ctx)
	}
	b.WriteString("</main>")
}

// indexToolResults maps toolCallID -> latest result across messages.
func indexToolResults(msgs []proto.Message) map[string]proto.ToolResult {
	out := make(map[string]proto.ToolResult)
	for i := range msgs {
		for _, tr := range msgs[i].ToolResults() {
			if tr.ToolCallID != "" {
				out[tr.ToolCallID] = tr
			}
		}
	}
	return out
}

// renderMessage writes one message section. Parts render into a
// scratch builder first: a message whose every part was consumed
// elsewhere (tool results already shown with their calls) is skipped
// entirely instead of leaving an empty "TOOL finished" shell behind.
func renderMessage(b *strings.Builder, m *proto.Message, ctx *turnRenderCtx) {
	parts := new(strings.Builder)
	for _, part := range m.Parts {
		renderPart(parts, part, ctx)
	}
	if parts.Len() == 0 {
		return
	}

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
	b.WriteString(parts.String())
	b.WriteString("</section>")
}

// renderPart renders one content part; parts that produce nothing
// (empty text, results already attached to their calls) write
// nothing.
func renderPart(b *strings.Builder, part proto.ContentPart, ctx *turnRenderCtx) {
	switch p := part.(type) {
	case proto.ReasoningContent:
		if p.Thinking == "" && p.Signature == "" {
			return
		}
		fmt.Fprintf(b, "<details class=\"thinking\"><summary>💭 Thinking</summary><pre>%s</pre></details>", esc(p.Thinking))
	case proto.TextContent:
		if p.Text == "" {
			return
		}
		fmt.Fprintf(b, "<div class=\"text\">%s</div>", markdownish(p.Text))
	case proto.ToolCall:
		renderToolCall(b, p, ctx)
	case proto.ToolResult:
		if ctx != nil && ctx.shown[p.ToolCallID] {
			// Already rendered together with its tool call.
			return
		}
		if ctx != nil {
			ctx.shown[p.ToolCallID] = true
		}
		cls := "toolresult"
		summary := "📤 " + p.Name
		if p.IsError {
			cls += " failed"
			summary += " (error)"
		}
		fmt.Fprintf(b, "<details class=\"%s\"><summary>%s</summary>", cls, esc(summary))
		renderResultBody(b, "结果", p)
		b.WriteString("</details>")
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

// Tool-name groups for specialized rendering. The edit-family names
// and sub-agent names mirror the server's tool registry.
var (
	editToolNames     = map[string]bool{"edit": true, "write": true}
	subAgentToolNames = map[string]bool{"task": true, "agent": true}
)

// renderToolCall renders a tool call with the best view for its
// family: colored diffs for edit/write/multiedit, input plus a
// nested sub-agent transcript for task/agent, and re-indented JSON
// for everything else. The call's matching tool result (by
// tool_call_id) is rendered inside the same block.
func renderToolCall(b *strings.Builder, p proto.ToolCall, ctx *turnRenderCtx) {
	input := parseToolInput(p.Input)
	switch {
	case editToolNames[p.Name]:
		renderEditToolCall(b, p, input, ctx)
	case p.Name == "multiedit":
		renderMultiEditToolCall(b, p, input, ctx)
	case subAgentToolNames[p.Name]:
		renderSubAgentToolCall(b, p, ctx)
	default:
		fmt.Fprintf(b, "<details class=\"toolcall\"><summary>🔧 %s</summary><pre class=\"code\">%s</pre>", esc(p.Name), esc(prettyJSON(p.Input)))
		attachToolResult(b, p.ID, resultLabel(p.Name), ctx)
		b.WriteString("</details>")
	}
}

// resultLabel labels the attached result block; sub-agent calls show
// their final answer as 输出.
func resultLabel(toolName string) string {
	if subAgentToolNames[toolName] {
		return "输出"
	}
	return "结果"
}

// attachToolResult renders the call's tool result, if any, marking it
// shown so the standalone tool-message rendering skips it.
func attachToolResult(b *strings.Builder, toolCallID, label string, ctx *turnRenderCtx) {
	if ctx == nil || toolCallID == "" {
		return
	}
	res, ok := ctx.results[toolCallID]
	if !ok {
		return
	}
	ctx.shown[toolCallID] = true
	renderResultBody(b, label, res)
}

// renderResultBody writes a labeled result block (status, content).
func renderResultBody(b *strings.Builder, label string, res proto.ToolResult) {
	head := label
	if res.IsError {
		head += "（出错）"
	}
	fmt.Fprintf(b, "<div class=\"hunk-head result-head%s\">📤 %s</div>", resultClass(res.IsError), esc(head))
	fmt.Fprintf(b, "<pre class=\"code\">%s</pre>", esc(truncate(res.Content, maxResultDisplay)))
}

func resultClass(isError bool) string {
	if isError {
		return " failed"
	}
	return ""
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
// old and new content, with the call's result attached.
func renderEditToolCall(b *strings.Builder, p proto.ToolCall, input map[string]any, ctx *turnRenderCtx) {
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
		esc(p.Name), esc(filePath), esc(kind))
	renderDiffHTML(b, oldText, newText)
	attachToolResult(b, p.ID, resultLabel(p.Name), ctx)
	b.WriteString("</details>")
}

// renderMultiEditToolCall renders each edit of a multiedit call as
// its own diff hunk, with the call's result attached.
func renderMultiEditToolCall(b *strings.Builder, p proto.ToolCall, input map[string]any, ctx *turnRenderCtx) {
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
	attachToolResult(b, p.ID, resultLabel(p.Name), ctx)
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
	attachToolResult(b, p.ID, resultLabel(p.Name), ctx)
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
table.diff td.ln { width: 3ch; text-align: right; color: #5b6472; user-select: none;
  border-right: 1px solid #262b36; padding-right: 6px; }
table.diff tr.ctx td { color: #8b93a3; background: #0e1118; }
table.diff tr.del td { background: #2a1418; color: #ff9daa; }
table.diff tr.add td { background: #122a19; color: #8ee2a9; }
table.diff tr.hunk td { background: #10141d; color: #6fb3ff; }
table.diff span.hl { background: #4a2c30; color: #ffd7dc; border-radius: 2px; }
table.diff tr.add span.hl { background: #24402c; color: #c9f2d6; }
.diffwrap { margin: 6px 0; }
.diff-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; font-size: 11.5px; }
.diff-toolbar .stat { font-weight: 600; padding: 0 6px; border-radius: 4px; }
.diff-toolbar .stat.add { color: #8ee2a9; background: #122a19; }
.diff-toolbar .stat.del { color: #ff9daa; background: #2a1418; }
.diff-switch { margin-left: auto; color: #9fb0c8; cursor: pointer; user-select: none; }
.diffwrap > input.diff-toggle { display: none; }
table.diff.split { display: none; table-layout: fixed; }
table.diff.split td { overflow-wrap: anywhere; }
.diffwrap > input:checked ~ table.diff.split { display: table; }
.diffwrap > input:checked ~ table.diff.unified { display: none; }
.diff-file { font: 11.5px ui-monospace, Menlo, Consolas, monospace; color: #b39ddb;
  background: #10141d; border: 1px solid #262b36; border-radius: 4px; padding: 3px 8px; margin: 8px 0 2px;
  white-space: pre-wrap; word-break: break-all; }
.hunk-head { color: #9fb0c8; font-size: 11.5px; margin: 8px 0 2px; }
.subagent-session { border: 1px dashed #33405a; border-radius: 6px; padding: 6px 10px; margin: 6px 0; }
.subagent-session > summary { color: #b39ddb; }
.text h1, .text h2, .text h3, .text h4, .text h5, .text h6 { margin: 12px 0 6px; line-height: 1.3; }
.text h1 { font-size: 18px; } .text h2 { font-size: 16px; } .text h3 { font-size: 15px; }
.text h4, .text h5, .text h6 { font-size: 14px; }
.text p { margin: 6px 0; }
.text ul, .text ol { margin: 6px 0; padding-left: 22px; }
.text li { margin: 2px 0; }
.text blockquote { margin: 6px 0; padding: 2px 12px; border-left: 3px solid #3b465c; color: #a8b2c2; }
.text hr { border: none; border-top: 1px solid #262b36; margin: 12px 0; }
.text del { color: #8b93a3; }
input.search { width: 100%; box-sizing: border-box; padding: 8px 10px; margin: 8px 0;
  background: #0b0d12; color: #d7dce4; border: 1px solid #2c3340; border-radius: 6px;
  font: 13px -apple-system, "Segoe UI", Roboto, "PingFang SC", sans-serif; }
input.search:focus { outline: 1px solid #3b6ea5; }
table.sessions { border-collapse: collapse; margin: 8px 0; font-size: 12.5px; width: 100%; }
table.sessions th, table.sessions td { border: 1px solid #2c3340; padding: 4px 8px; text-align: left; }
table.sessions th { background: #171c26; color: #c7d0dd; font-weight: 600; white-space: nowrap; }
table.sessions tbody tr:nth-child(odd) td { background: #12151d; }
table.sessions tr.current td { background: #14202b; }
footer.session-footer { border-top: 1px solid #262b36; margin-top: 16px; padding-top: 10px; }
footer.session-footer .sf-title { font-weight: 600; font-size: 13px; margin-bottom: 6px; }
table.sf { border-collapse: collapse; font-size: 12px; width: 100%; }
table.sf td { border: 1px solid #232a36; padding: 3px 10px; }
table.sf td:first-child { color: #8b93a3; white-space: nowrap; width: 1%; text-align: right; }
.sf-bar { display: inline-block; vertical-align: middle; width: 120px; height: 8px;
  margin-left: 8px; background: #0b0d12; border: 1px solid #262b36; border-radius: 4px; overflow: hidden; }
.sf-bar > span { display: block; height: 100%; background: #43d9a3; }
.sf-bar.high > span { background: #ffc46b; }
.sf-bar.critical > span { background: #ff8080; }
table.md { border-collapse: collapse; margin: 8px 0; font-size: 13px; width: 100%; }
table.md th, table.md td { border: 1px solid #2c3340; padding: 4px 10px; text-align: left; }
table.md th { background: #171c26; color: #c7d0dd; font-weight: 600; }
table.md tbody tr:nth-child(odd) td { background: #12151d; }
`
