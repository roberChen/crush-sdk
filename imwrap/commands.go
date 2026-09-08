package imwrap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/roberChen/crush-sdk/proto"
)

// ErrCommandExists is returned by RegisterCommand when the name is
// already taken by a builtin or a previously registered command.
var ErrCommandExists = errors.New("command already registered")

// CommandFunc is a custom command handler.
type CommandFunc func(ctx context.Context, c CommandContext) error

// command is the internal registry entry.
type command struct {
	name       string
	desc       string
	fn         CommandFunc
	builtin    bool
	builtinAlt []string // aliases, also registered
}

// CommandContext carries everything a command handler needs: the
// wrapper, the originating chat, the parsed arguments, and the
// chat's current session.
type CommandContext struct {
	// W is the wrapper that dispatched the command.
	W *Wrapper
	// ChatID is the IM conversation the command came from.
	ChatID string
	// Name is the command name (without prefix, lowercased).
	Name string
	// Args are the whitespace-separated arguments.
	Args []string
	// ArgText is the raw argument text (preserves inner spacing).
	ArgText string
	// SessionID is the chat's current session, or "" when the chat
	// has no session yet. Use W.CreateSession to create and bind one
	// (an empty session is also created lazily by the next prompt).
	SessionID string
}

// Reply sends a text message to the originating chat.
func (c CommandContext) Reply(ctx context.Context, text string) error {
	return c.W.sendText(ctx, c.ChatID, text)
}

// ReplyFile sends a file to the originating chat.
func (c CommandContext) ReplyFile(ctx context.Context, filename string, content []byte) error {
	return c.W.sendFile(ctx, c.ChatID, filename, content)
}

// CommandInfo describes a registered command.
type CommandInfo struct {
	// Name is the command name without the prefix.
	Name string
	// Description is a one-line usage summary.
	Description string
	// Builtin marks commands shipped with the wrapper.
	Builtin bool
}

// RegisterCommand registers a custom command. It fails with
// [ErrCommandExists] when the name collides with a builtin or an
// existing command. Commands run on the caller's goroutine (inside
// HandleMessage) and may reply via [CommandContext.Reply].
func (w *Wrapper) RegisterCommand(name, description string, fn CommandFunc) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || fn == nil {
		return errors.New("imwrap: command name and handler are required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.cmds[name]; ok {
		return fmt.Errorf("%w: /%s", ErrCommandExists, name)
	}
	w.cmds[name] = &command{name: name, desc: description, fn: fn}
	return nil
}

// UnregisterCommand removes a custom command. Builtins cannot be
// removed; attempting to returns an error.
func (w *Wrapper) UnregisterCommand(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	w.mu.Lock()
	defer w.mu.Unlock()
	cmd, ok := w.cmds[name]
	if !ok {
		return fmt.Errorf("command not registered: /%s", name)
	}
	if cmd.builtin {
		return fmt.Errorf("cannot unregister builtin command: /%s", name)
	}
	delete(w.cmds, name)
	return nil
}

// Commands lists registered commands (builtins first, then customs
// alphabetically).
func (w *Wrapper) Commands() []CommandInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []CommandInfo
	for _, cmd := range w.cmds {
		if cmd.builtin {
			out = append(out, CommandInfo{Name: cmd.name, Description: cmd.desc, Builtin: true})
		}
	}
	var customs []string
	for name := range w.cmds {
		if !w.cmds[name].builtin {
			customs = append(customs, name)
		}
	}
	sort.Strings(customs)
	for _, name := range customs {
		out = append(out, CommandInfo{Name: name, Description: w.cmds[name].desc})
	}
	return out
}

// runCommand dispatches a parsed command message.
func (w *Wrapper) runCommand(ctx context.Context, chatID string, p Parsed) error {
	w.mu.Lock()
	cmd, ok := w.cmds[p.Name]
	var sessionID string
	if st, exists := w.chats[chatID]; exists {
		sessionID = st.sessionID
	}
	w.mu.Unlock()
	if !ok {
		return w.sendText(ctx, chatID, "未知命令 /"+p.Name+"。可用命令：\n"+w.commandHelp())
	}
	return cmd.fn(ctx, CommandContext{
		W:         w,
		ChatID:    chatID,
		Name:      p.Name,
		Args:      p.Args,
		ArgText:   p.ArgText,
		SessionID: sessionID,
	})
}

// commandHelp renders the command list shown by /help and unknown
// commands.
func (w *Wrapper) commandHelp() string {
	prefix := w.cfg.CommandPrefix
	var b strings.Builder
	for _, ci := range w.Commands() {
		fmt.Fprintf(&b, "%s%s - %s\n", prefix, ci.Name, ci.Description)
	}
	return b.String()
}

// registerBuiltinCommands installs the shipped commands.
func registerBuiltinCommands(w *Wrapper) {
	builtins := []struct {
		name string
		desc string
		alt  []string
		fn   CommandFunc
	}{
		{"help", "显示可用命令", []string{"?"}, cmdHelp},
		{"sessions", "列出全部会话为 HTML（可搜索，/sessions [关键词]）", []string{"ls"}, cmdSessions},
		{"switch", "切换会话 (/switch <序号|会话ID前缀>)", nil, cmdSwitch},
		{"new", "新建会话 (/new [-d 目录] [标题])", nil, cmdNew},
		{"info", "查看当前会话信息（目录/技能/工具/上下文水位）", nil, cmdInfo},
		{"ask", "一次性对话 (/ask [-m 模型] [-d 目录] 提示词)，结束公布 session id", nil, cmdAsk},
		{"say", "向指定会话发一条消息 (/say <会话ID> 提示词)，不影响当前绑定", nil, cmdSay},
		{"models", "列出可用模型", nil, cmdModels},
		{"model", "查看或设置当前模型 (/model [provider/model])", nil, cmdModel},
		{"export", "导出当前会话完整记录为 HTML 文件", nil, cmdExport},
		{"summarize", "手动压缩当前会话（生成摘要释放上下文）", nil, cmdSummarize},
		{"status", "查看当前会话与任务状态", nil, cmdStatus},
		{"cancel", "取消当前问题或正在执行的任务", nil, cmdCancel},
	}
	for _, b := range builtins {
		entry := &command{name: b.name, desc: b.desc, fn: b.fn, builtin: true, builtinAlt: b.alt}
		w.cmds[b.name] = entry
		for _, alt := range b.alt {
			w.cmds[alt] = entry
		}
	}
}

func cmdHelp(ctx context.Context, c CommandContext) error {
	return c.Reply(ctx, "可用命令：\n"+c.W.commandHelp())
}

// sessionEntry pairs a session with its owning workspace for the
// cross-directory /sessions listing.
type sessionEntry struct {
	sess proto.Session
	wsID string
	path string
}

// listAllSessions aggregates sessions from every known workspace,
// most recently updated first.
func (w *Wrapper) listAllSessions(ctx context.Context) ([]sessionEntry, error) {
	var out []sessionEntry
	for _, wsID := range w.workspaceIDs() {
		sessions, err := w.client.ListSessions(ctx, wsID)
		if err != nil {
			return nil, fmt.Errorf("列出会话失败: %w", err)
		}
		path := w.pathForWS(wsID)
		for _, s := range sessions {
			out = append(out, sessionEntry{sess: s, wsID: wsID, path: path})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].sess.UpdatedAt != out[j].sess.UpdatedAt {
			return out[i].sess.UpdatedAt > out[j].sess.UpdatedAt
		}
		return out[i].sess.ID < out[j].sess.ID
	})
	return out, nil
}

func cmdSessions(ctx context.Context, c CommandContext) error {
	keyword := strings.TrimSpace(c.ArgText)
	summaries, err := c.W.SessionSummaries(ctx, c.ChatID, keyword)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		if keyword != "" {
			return c.Reply(ctx, "没有匹配 "+keyword+" 的会话。")
		}
		return c.Reply(ctx, "当前没有任何会话。")
	}

	name := fmt.Sprintf("crush-sessions-%s.html", timestampSlug(now()))
	if err := c.ReplyFile(ctx, name, RenderSessionsHTML(summaries, keyword)); err != nil {
		return err
	}
	total := len(summaries)
	if keyword != "" {
		return c.Reply(ctx, fmt.Sprintf("📋 关键词 %s 匹配 %d 个会话，详见 HTML 文件（文件内可继续搜索；序号可用于 /switch）。", keyword, total))
	}
	return c.Reply(ctx, fmt.Sprintf("📋 共 %d 个会话，详见 HTML 文件（文件内可搜索；序号可用于 /switch）。", total))
}

func cmdSwitch(ctx context.Context, c CommandContext) error {
	if len(c.Args) == 0 {
		return c.Reply(ctx, "用法: /switch <序号|会话ID前缀>（序号见 /sessions）")
	}
	entries, err := c.W.listAllSessions(ctx)
	if err != nil {
		return err
	}

	target, err := resolveSessionArg(entries, c.Args[0])
	if err != nil {
		return c.Reply(ctx, err.Error())
	}

	c.W.mu.Lock()
	st := c.W.state(c.ChatID)
	st.sessionID = target.sess.ID
	st.wsID = target.wsID
	c.W.mu.Unlock()
	// Presence hint for other clients; ignore failures.
	_ = c.W.client.SetCurrentSession(ctx, target.wsID, target.sess.ID)
	title := target.sess.Title
	if title == "" {
		title = "(无标题)"
	}
	return c.Reply(ctx, fmt.Sprintf("已切换到会话 %s（%s，%d 条消息，目录 %s）",
		shortID(target.sess.ID), title, target.sess.MessageCount, target.path))
}

// resolveSessionArg resolves a user-supplied token (1-based index
// from /sessions, or a session ID prefix) to a session entry.
func resolveSessionArg(entries []sessionEntry, token string) (*sessionEntry, error) {
	if n, err := strconv.Atoi(token); err == nil {
		if n >= 1 && n <= len(entries) {
			return &entries[n-1], nil
		}
		return nil, fmt.Errorf("序号 %d 超出范围 (1-%d)", n, len(entries))
	}
	lower := strings.ToLower(token)
	var matches []*sessionEntry
	for i := range entries {
		if strings.HasPrefix(strings.ToLower(entries[i].sess.ID), lower) {
			matches = append(matches, &entries[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("没有匹配 %q 的会话", token)
	default:
		return nil, fmt.Errorf("%q 匹配多个会话，请提供更长的 ID 前缀", token)
	}
}

// parseFlagArgs splits leading "-x value"/"--xx value" flags from the
// rest of an argument list. Unknown flags stop flag parsing (so they
// can be part of the prompt text).
func parseFlagArgs(args []string) (flags map[string]string, rest []string) {
	flags = make(map[string]string)
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return flags, args[i:]
		}
		name := strings.TrimLeft(tok, "-")
		if name == "" {
			return flags, args[i:]
		}
		if i+1 < len(args) {
			flags[name] = args[i+1]
			i++
			continue
		}
		flags[name] = ""
	}
	return flags, nil
}

func flagValue(flags map[string]string, names ...string) string {
	for _, n := range names {
		if v, ok := flags[n]; ok {
			return v
		}
	}
	return ""
}

func cmdNew(ctx context.Context, c CommandContext) error {
	flags, rest := parseFlagArgs(c.Args)
	dir := flagValue(flags, "d", "dir")
	title := strings.Join(rest, " ")
	sess, err := c.W.CreateSession(ctx, c.ChatID, dir, title)
	if err != nil {
		return err
	}
	wsPath := c.W.pathForWS(c.W.chatWorkspace(c.ChatID))
	return c.Reply(ctx, fmt.Sprintf("已创建并切换到新会话 %s（%s，目录 %s）",
		shortID(sess.ID), sess.Title, wsPath))
}

func cmdInfo(ctx context.Context, c CommandContext) error {
	info, err := c.W.GetSessionInfo(ctx, c.ChatID)
	if err != nil {
		return err
	}
	return c.Reply(ctx, FormatSessionInfo(info))
}

func cmdAsk(ctx context.Context, c CommandContext) error {
	flags, rest := parseFlagArgs(c.Args)
	prompt := strings.Join(rest, " ")
	if prompt == "" {
		return c.Reply(ctx, "用法: /ask [-m provider/model] [-d 目录] [-t 标题] 提示词")
	}
	return c.W.AskOnce(ctx, c.ChatID, prompt, AskOptions{
		Model: flagValue(flags, "m", "model"),
		Dir:   flagValue(flags, "d", "dir"),
		Title: flagValue(flags, "t", "title"),
	})
}

func cmdSay(ctx context.Context, c CommandContext) error {
	if len(c.Args) < 2 {
		return c.Reply(ctx, "用法: /say <会话ID或前缀> 提示词")
	}
	sessionID := c.Args[0]
	prompt := strings.TrimSpace(c.ArgText)
	prompt = strings.TrimSpace(strings.TrimPrefix(prompt, sessionID))
	if prompt == "" {
		return c.Reply(ctx, "用法: /say <会话ID或前缀> 提示词")
	}
	return c.W.AskInSession(ctx, c.ChatID, sessionID, prompt)
}

func cmdModels(ctx context.Context, c CommandContext) error {
	models, err := c.W.ListModels(ctx, c.ChatID)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return c.Reply(ctx, "没有已配置的 provider/模型。")
	}
	var b strings.Builder
	lastProvider := ""
	for _, m := range models {
		if m.Provider != lastProvider {
			label := m.Provider
			if m.ProviderName != "" && m.ProviderName != m.Provider {
				label += " (" + m.ProviderName + ")"
			}
			fmt.Fprintf(&b, "%s:\n", label)
			lastProvider = m.Provider
		}
		marker := "  "
		if m.Current {
			marker = "▶ "
		}
		name := m.Name
		if name == "" || name == m.ID {
			fmt.Fprintf(&b, "%s%s (ctx %s)\n", marker, m.ID, humanTokens(m.ContextWindow))
			continue
		}
		fmt.Fprintf(&b, "%s%s - %s (ctx %s)\n", marker, m.ID, name, humanTokens(m.ContextWindow))
	}
	return c.Reply(ctx, b.String())
}

// humanTokens renders a token count compactly (200k, 1.2M).
func humanTokens(n int64) string {
	switch {
	case n <= 0:
		return "?"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func cmdModel(ctx context.Context, c CommandContext) error {
	if strings.TrimSpace(c.ArgText) == "" {
		wsID := c.W.chatWorkspace(c.ChatID)
		agent, err := c.W.client.GetAgentInfo(ctx, wsID)
		if err != nil || agent == nil || agent.IsZero() {
			return c.Reply(ctx, "无法获取当前模型信息。")
		}
		return c.Reply(ctx, fmt.Sprintf("当前模型: %s/%s（%s，上下文 %s）",
			agent.ModelCfg.Provider, agent.ModelCfg.Model, agent.Model.Name, humanTokens(agent.Model.ContextWindow)))
	}
	info, err := c.W.SetModel(ctx, c.ChatID, c.ArgText)
	if err != nil {
		return c.Reply(ctx, "设置模型失败: "+err.Error())
	}
	return c.Reply(ctx, "✅ 已切换模型: "+info.Spec())
}

// ExportSessionHTML renders the full conversation of the chat's
// current session and sends it to the chat as an HTML file. It backs
// the /export builtin and is also usable programmatically.
func (w *Wrapper) ExportSessionHTML(ctx context.Context, chatID string) error {
	sessionID, wsID, err := w.ensureSession(ctx, chatID)
	if err != nil {
		return err
	}
	msgs, err := w.client.ListMessages(ctx, wsID, sessionID)
	if err != nil {
		return fmt.Errorf("failed to list session messages: %w", err)
	}
	var sess *proto.Session
	if s, err := w.client.GetSession(ctx, wsID, sessionID); err == nil {
		sess = s
	}
	name := fmt.Sprintf("crush-session-%s-export-%s.html", shortID(sessionID), timestampSlug(now()))
	if err := w.sendFile(ctx, chatID, name, RenderSessionHTML(sess, msgs)); err != nil {
		return err
	}
	title := ""
	if sess != nil {
		title = sess.Title
	}
	return w.sendText(ctx, chatID, fmt.Sprintf("📦 已导出会话 %s（%s）共 %d 条消息。", shortID(sessionID), title, len(msgs)))
}

func cmdExport(ctx context.Context, c CommandContext) error {
	return c.W.ExportSessionHTML(ctx, c.ChatID)
}

// SummarizeSession requests a manual summarization of the chat's
// current session, like the TUI's summarize action: the server
// generates a summary message so the context window is released.
func (w *Wrapper) SummarizeSession(ctx context.Context, chatID string) error {
	sessionID, wsID, err := w.ensureSession(ctx, chatID)
	if err != nil {
		return err
	}
	if err := w.client.AgentSummarizeSession(ctx, wsID, sessionID); err != nil {
		return fmt.Errorf("failed to summarize session: %w", err)
	}
	return w.sendText(ctx, chatID, "🗂 已请求压缩会话 "+shortID(sessionID)+"，摘要生成后会作为 summary 消息出现。")
}

func cmdSummarize(ctx context.Context, c CommandContext) error {
	return c.W.SummarizeSession(ctx, c.ChatID)
}

func cmdStatus(ctx context.Context, c CommandContext) error {
	w := c.W
	w.mu.Lock()
	st := w.state(c.ChatID)
	sessionID, wsID := st.sessionID, st.wsID
	busy, runID, queued := st.busy, st.runID, len(st.queued)
	pending := st.pendingQ != nil
	w.mu.Unlock()

	var b strings.Builder
	if sessionID == "" {
		b.WriteString("当前没有绑定会话，下一条消息将自动创建。\n")
	} else {
		fmt.Fprintf(&b, "当前会话: %s（目录 %s）\n", shortID(sessionID), w.pathForWS(wsID))
	}
	fmt.Fprintf(&b, "任务进行中: %v\n", busy)
	if runID != "" {
		fmt.Fprintf(&b, "run: %s\n", shortID(runID))
	}
	fmt.Fprintf(&b, "排队消息: %d\n", queued)
	fmt.Fprintf(&b, "等待回答: %v\n", pending)
	fmt.Fprintf(&b, "workspace: %s\n", shortID(w.WorkspaceID()))
	return c.Reply(ctx, b.String())
}

func cmdCancel(ctx context.Context, c CommandContext) error {
	w := c.W
	w.mu.Lock()
	st := w.state(c.ChatID)
	pending := st.pendingQ
	activeSession, activeWS := st.activeSession, st.activeWS
	sessionID := st.sessionID
	busy := st.busy
	w.mu.Unlock()

	switch {
	case pending != nil:
		if _, err := w.client.CancelQuestionBatch(ctx, pending.wsID); err != nil {
			return fmt.Errorf("取消问题失败: %w", err)
		}
		w.clearPendingQuestion(c.ChatID)
		return c.Reply(ctx, "已取消当前问题。")

	case busy && activeSession != "" && activeWS != "":
		if err := w.client.CancelAgentSession(ctx, activeWS, activeSession); err != nil {
			return fmt.Errorf("取消任务失败: %w", err)
		}
		return c.Reply(ctx, "已请求取消当前任务。")

	case busy && sessionID != "":
		if err := w.client.CancelAgentSession(ctx, w.chatWorkspace(c.ChatID), sessionID); err != nil {
			return fmt.Errorf("取消任务失败: %w", err)
		}
		return c.Reply(ctx, "已请求取消当前任务。")

	default:
		return c.Reply(ctx, "没有进行中的任务或待回答的问题。")
	}
}
