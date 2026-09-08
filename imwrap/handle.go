package imwrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/roberChen/crush-sdk/config"
	"github.com/roberChen/crush-sdk/proto"
)

// now indirection keeps file timestamp generation stub-friendly.
var now = time.Now

// HandleMessage processes one incoming IM message: it first drops
// the bot's own output (message-ID marks, Config.SelfAccount, or the
// content echo filter for shared-account setups), then parses the
// text, routes commands (builtins and registered custom commands
// share one namespace), routes pending-question answers, and starts
// an agent turn for anything else.
//
// HandleMessage returns once the prompt has been accepted by the
// server (or the command has finished replying); an immediate
// acknowledgement is sent through the adapter at that point, and the
// turn's HTML report and summary arrive asynchronously as the run
// completes. Hosts should call it from one goroutine per chat (or
// sequentially) to keep per-chat ordering.
func (w *Wrapper) HandleMessage(ctx context.Context, msg IMMessage) error {
	if w.isSelfMessage(msg) {
		w.log.Debug("imwrap: dropping self output", "chat", msg.ChatID, "id", msg.ID)
		return nil
	}
	p := Parse(w.cfg.CommandPrefix, msg.Text)
	switch p.Kind {
	case KindIgnore:
		return nil

	case KindCommand:
		if err := w.runCommand(ctx, msg.ChatID, p); err != nil {
			return w.notifyError(msg.ChatID, "command /"+p.Name+" failed", err)
		}
		return nil

	default: // KindAgent
		if q := w.pendingQuestionFor(msg.ChatID); q != nil {
			return w.answerQuestion(ctx, msg.ChatID, q, p.Raw)
		}
		if err := w.startAgentTurn(ctx, msg.ChatID, p.Raw); err != nil {
			return w.notifyError(msg.ChatID, "failed to start agent", err)
		}
		return nil
	}
}

// isSelfMessage reports whether an incoming IM message is the bot's
// own output rather than user input.
func (w *Wrapper) isSelfMessage(msg IMMessage) bool {
	if w.cfg.DisableEchoFilter {
		return false
	}
	if msg.FromSelf {
		return true
	}
	if w.cfg.SelfAccount != "" && msg.Sender == w.cfg.SelfAccount {
		return true
	}
	if w.cfg.EchoWindow < 0 {
		// Content echo matching disabled; IDs and sender still apply.
		return w.echo.matchIDOnly(msg.ChatID, msg.ID)
	}
	return w.echo.match(msg.ChatID, msg.ID, msg.Text, msg.SentAt)
}

// MarkSelfMessage records the IM-side message ID of a message the
// bot itself sent (the send path records texts automatically; hosts
// that know the resulting message ID should also mark it for exact
// filtering). It is the most reliable self-output filter.
func (w *Wrapper) MarkSelfMessage(chatID, messageID string) {
	w.echo.markID(chatID, messageID)
}

// IsSelfOutput reports whether an incoming message would be treated
// as the bot's own output under the current filters. Hosts can use it
// for their own bookkeeping.
func (w *Wrapper) IsSelfOutput(msg IMMessage) bool {
	return w.isSelfMessage(msg)
}

// sendText sends a text through the adapter and records it for echo
// filtering. All wrapper output goes through here.
func (w *Wrapper) sendText(ctx context.Context, chatID, text string) error {
	err := w.adapter.SendText(ctx, chatID, text)
	if err == nil {
		w.echo.record(chatID, text)
	}
	return err
}

// sendFile sends a file through the adapter. Filenames are recorded
// for potential echo matching; file bodies are not.
func (w *Wrapper) sendFile(ctx context.Context, chatID, filename string, content []byte) error {
	return w.adapter.SendFile(ctx, chatID, filename, content)
}

// notifyError reports an error to a chat and returns it.
func (w *Wrapper) notifyError(chatID, what string, err error) error {
	w.log.Error("imwrap: "+what, "chat", chatID, "error", err)
	if ctx, cerr := w.runCtx(); cerr == nil {
		text := "⚠ " + what + ": " + err.Error()
		if sendErr := w.sendText(ctx, chatID, text); sendErr != nil {
			w.log.Error("imwrap: failed to deliver error notice", "chat", chatID, "error", sendErr)
		}
	}
	return err
}

// ensureSession returns the session (and its workspace) a chat
// should run on, creating one in the default workspace when the chat
// has none (or its session vanished).
func (w *Wrapper) ensureSession(ctx context.Context, chatID string) (sessionID, wsID string, err error) {
	w.mu.Lock()
	st := w.state(chatID)
	sid, ws := st.sessionID, st.wsID
	w.mu.Unlock()

	if sid != "" {
		if _, gerr := w.client.GetSession(ctx, ws, sid); gerr == nil {
			return sid, ws, nil
		}
		// Fall through to creating a fresh one.
	}

	wsID, err = w.workspaceFor(ctx, w.client.Path())
	if err != nil {
		return "", "", err
	}
	sess, cerr := w.client.CreateSession(ctx, wsID, w.cfg.SessionTitle)
	if cerr != nil {
		return "", "", fmt.Errorf("failed to create session: %w", cerr)
	}
	if serr := w.client.SetCurrentSession(ctx, wsID, sess.ID); serr != nil {
		w.log.Debug("imwrap: SetCurrentSession failed", "error", serr)
	}
	w.mu.Lock()
	st = w.state(chatID)
	st.sessionID = sess.ID
	st.wsID = wsID
	w.mu.Unlock()
	return sess.ID, wsID, nil
}

// CreateSession creates a new session for a chat, optionally in a
// different directory (which transparently creates or reuses that
// directory's workspace), binds the chat to it, and returns it.
// An empty dir means the client's default path.
func (w *Wrapper) CreateSession(ctx context.Context, chatID, dir, title string) (*proto.Session, error) {
	if dir == "" {
		dir = w.client.Path()
	}
	wsID, err := w.workspaceFor(ctx, dir)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = w.cfg.SessionTitle
	}
	sess, err := w.client.CreateSession(ctx, wsID, title)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	w.mu.Lock()
	st := w.state(chatID)
	st.sessionID = sess.ID
	st.wsID = wsID
	w.mu.Unlock()
	// Presence hint for other clients; ignore failures.
	_ = w.client.SetCurrentSession(ctx, wsID, sess.ID)
	return sess, nil
}

// startAgentTurn sends a prompt to the agent on the chat's session
// and acknowledges immediately. When the session is busy the prompt
// is queued and run after the in-flight turn completes.
func (w *Wrapper) startAgentTurn(ctx context.Context, chatID, prompt string) error {
	w.mu.Lock()
	st := w.state(chatID)
	if st.busy {
		st.queued = append(st.queued, prompt)
		pos := len(st.queued)
		w.mu.Unlock()
		return w.sendText(ctx, chatID, fmt.Sprintf("⏳ 正在处理上一条消息，已加入队列（第 %d 位）。/cancel 可中止当前任务。", pos))
	}
	// Tentatively claim the busy slot so concurrent senders cannot
	// interleave; rolled back below on failure.
	st.busy = true
	w.mu.Unlock()

	sessionID, wsID, err := w.ensureSession(ctx, chatID)
	if err != nil {
		w.mu.Lock()
		st.busy = false
		w.mu.Unlock()
		return err
	}

	run := &runState{chatID: chatID, wsID: wsID, sessionID: sessionID, prompt: prompt, attached: true}
	runID := newRunID()
	w.mu.Lock()
	w.registerRun(run, runID)
	st = w.state(chatID)
	st.runID = runID
	st.activeSession = sessionID
	st.activeWS = wsID
	w.mu.Unlock()

	if err := w.ackRun(ctx, chatID, run); err != nil {
		w.abortAttachedRun(chatID, runID)
		return fmt.Errorf("failed to send message to agent: %w", err)
	}
	return nil
}

// ackRun submits the prompt to the agent and, on acceptance, sends
// the immediate acknowledgement text. It is the last fallible step
// of starting a run.
func (w *Wrapper) ackRun(ctx context.Context, chatID string, run *runState) error {
	err := w.client.SendMessage(ctx, run.wsID, run.sessionID, run.runID, run.prompt)
	if err != nil {
		return err
	}
	ack := "🤖 已收到，正在处理…（/cancel 取消）"
	if run.reportSession {
		ack = "🎯 一次性对话已启动，结束后公布 session id（/cancel 取消）"
	} else if !run.attached {
		ack = fmt.Sprintf("📨 已发送到会话 %s（当前会话绑定不变，/cancel 取消）", shortID(run.sessionID))
	}
	if serr := w.sendText(ctx, chatID, ack); serr != nil {
		w.log.Warn("imwrap: failed to send acknowledgement", "chat", chatID, "error", serr)
	}
	return nil
}

// abortAttachedRun rolls back an attached run that failed to start.
func (w *Wrapper) abortAttachedRun(chatID, runID string) {
	w.dropRun(runID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if st, ok := w.chats[chatID]; ok && st.runID == runID {
		st.busy = false
		st.runID = ""
		st.activeSession = ""
		st.activeWS = ""
	}
}

// AskOptions parameterize [Wrapper.AskOnce].
type AskOptions struct {
	// Model optionally overrides the workspace default model for the
	// one-shot, as "provider/model" or a bare model id. The previous
	// default is restored when the run completes.
	Model string
	// Dir optionally runs the one-shot in another directory's
	// workspace. Empty means the client's default path.
	Dir string
	// Title optionally names the one-shot session.
	Title string
}

// AskOnce runs a one-shot conversation in a freshly created session
// (optionally in another directory and/or with a model override) and
// reports the session ID when the run completes. The chat's session
// binding is untouched: subsequent prompts continue on the previous
// session.
//
// The model override temporarily changes the workspace default model
// for the duration of the run; concurrent runs in the same workspace
// may observe it. It is restored as soon as the one-shot finishes.
func (w *Wrapper) AskOnce(ctx context.Context, chatID, prompt string, opts AskOptions) error {
	dir := opts.Dir
	if dir == "" {
		dir = w.client.Path()
	}
	wsID, err := w.workspaceFor(ctx, dir)
	if err != nil {
		return err
	}

	run := &runState{
		chatID:        chatID,
		wsID:          wsID,
		prompt:        prompt,
		reportSession: true,
	}
	if opts.Model != "" {
		prev, perr := w.currentModel(ctx, wsID)
		if perr != nil {
			return perr
		}
		sel, rerr := w.resolveModelSpec(ctx, wsID, opts.Model)
		if rerr != nil {
			return rerr
		}
		if err := w.client.UpdatePreferredModel(ctx, wsID, config.ScopeWorkspace, config.SelectedModelTypeLarge, sel); err != nil {
			return fmt.Errorf("failed to set one-shot model: %w", err)
		}
		run.modelOverride = true
		run.prevModel = prev
	}

	title := opts.Title
	if title == "" {
		title = "ask-once " + timestampSlug(now())
	}
	sess, err := w.client.CreateSession(ctx, wsID, title)
	if err != nil {
		w.restoreModel(ctx, run)
		return fmt.Errorf("failed to create one-shot session: %w", err)
	}
	run.sessionID = sess.ID

	runID := newRunID()
	w.mu.Lock()
	w.registerRun(run, runID)
	w.mu.Unlock()

	if err := w.ackRun(ctx, chatID, run); err != nil {
		w.dropRun(runID)
		w.restoreModel(ctx, run)
		return fmt.Errorf("failed to send message to agent: %w", err)
	}
	return nil
}

// AskInSession runs a one-off prompt on the given session without
// changing the chat's binding: after the run completes, subsequent
// prompts go back to the chat's current session. The session is
// located across all known workspaces; a prefix of its ID is enough
// when unambiguous.
func (w *Wrapper) AskInSession(ctx context.Context, chatID, sessionID, prompt string) error {
	fullID, wsID, err := w.locateSession(ctx, sessionID)
	if err != nil {
		return err
	}

	run := &runState{
		chatID:    chatID,
		wsID:      wsID,
		sessionID: fullID,
		prompt:    prompt,
	}
	runID := newRunID()
	w.mu.Lock()
	w.registerRun(run, runID)
	w.mu.Unlock()

	if err := w.ackRun(ctx, chatID, run); err != nil {
		w.dropRun(runID)
		return fmt.Errorf("failed to send message to agent: %w", err)
	}
	return nil
}

// locateSession resolves a session ID (or unambiguous prefix) to its
// full ID and owning workspace, searching every known workspace.
func (w *Wrapper) locateSession(ctx context.Context, sessionID string) (string, string, error) {
	if sessionID == "" {
		return "", "", fmt.Errorf("session id is required")
	}
	var prefixMatches []struct{ id, wsID string }
	for _, wsID := range w.workspaceIDs() {
		sessions, err := w.client.ListSessions(ctx, wsID)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			switch {
			case s.ID == sessionID:
				return s.ID, wsID, nil
			case strings.HasPrefix(s.ID, sessionID):
				prefixMatches = append(prefixMatches, struct{ id, wsID string }{s.ID, wsID})
			}
		}
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0].id, prefixMatches[0].wsID, nil
	case 0:
		return "", "", fmt.Errorf("session %q not found in any known workspace", sessionID)
	default:
		return "", "", fmt.Errorf("session prefix %q is ambiguous (%d matches)", sessionID, len(prefixMatches))
	}
}

// restoreModel reverts a one-shot model override. Failures are
// logged, not fatal.
func (w *Wrapper) restoreModel(ctx context.Context, run *runState) {
	if !run.modelOverride {
		return
	}
	if err := w.client.UpdatePreferredModel(ctx, run.wsID, config.ScopeWorkspace, config.SelectedModelTypeLarge, run.prevModel); err != nil {
		w.log.Error("imwrap: failed to restore previous model", "workspace", run.wsID, "error", err)
	}
}

// onRunComplete finalizes a run: it snapshots the collected messages,
// releases the chat's busy slot (attached runs only), sends the HTML
// report, restores any one-shot model override, reports the session
// ID for one-shots, and runs the next queued prompt if any.
func (w *Wrapper) onRunComplete(rc proto.RunComplete) {
	w.mu.Lock()
	var (
		run    *runState
		queued []string
	)
	if rc.RunID != "" {
		run = w.runs[rc.RunID]
		delete(w.runs, rc.RunID)
	} else {
		// Legacy servers without RunID correlation: match the busy
		// attached chat for the session.
		for id, r := range w.runs {
			if r.attached && r.sessionID == rc.SessionID {
				run = r
				delete(w.runs, id)
				break
			}
		}
	}
	if run == nil {
		// Not ours (another client's run) or already finalized.
		w.mu.Unlock()
		return
	}
	if run.attached {
		if st, ok := w.chats[run.chatID]; ok && st.runID == run.runID {
			queued = st.queued
			st.queued = nil
			st.busy = false
			st.runID = ""
			st.activeSession = ""
			st.activeWS = ""
			st.pendingQ = nil
		}
	}
	turn := w.turns[rc.SessionID]
	var msgs []proto.Message
	prompt := ""
	if turn != nil {
		msgs = turn.snapshot()
		prompt = turn.prompt
	}
	delete(w.turns, rc.SessionID)
	w.mu.Unlock()

	chatID := run.chatID
	w.wg.Go(func() {
		ctx, err := w.runCtx()
		if err != nil {
			return
		}

		if len(msgs) == 0 {
			// The SSE stream may have missed the turn (for example
			// after a reconnect); fall back to the stored history.
			if fetched, ferr := w.client.ListMessages(ctx, run.wsID, rc.SessionID); ferr == nil {
				msgs = fetched
			}
		}
		subs := w.subAgentTranscripts(ctx, run.wsID, rc.SessionID, msgs)

		name := fmt.Sprintf("crush-reply-%s.html", timestampSlug(now()))
		html := RenderTurnHTML(prompt, msgs, &rc, WithSubAgents(subs))
		if err := w.sendFile(ctx, chatID, name, html); err != nil {
			w.notifyError(chatID, "failed to send reply file", err)
		}

		notice := turnSummary(msgs, rc)
		if run.reportSession {
			notice += fmt.Sprintf("\n🆔 本次一次性对话 session id: %s（/switch 可切换过去）", rc.SessionID)
		}
		if rc.Error != "" {
			notice = "⚠ 本轮执行出错: " + truncate(rc.Error, 300)
		}
		if err := w.sendText(ctx, chatID, notice); err != nil {
			w.log.Error("imwrap: failed to send turn summary", "chat", chatID, "error", err)
		}

		w.restoreModel(ctx, run)

		if run.attached && len(queued) > 0 {
			next := queued[0]
			rest := queued[1:]
			if err := w.startAgentTurn(ctx, chatID, next); err != nil {
				w.notifyError(chatID, "failed to start queued prompt", err)
			}
			for _, p := range rest {
				w.mu.Lock()
				w.state(chatID).queued = append(w.state(chatID).queued, p)
				w.mu.Unlock()
			}
		}
	})
}

// subAgentTranscripts loads the child sessions spawned by the
// turn's task/agent tool calls (matched by ParentSessionID and
// creation time) with their messages, oldest first. Sub-agent runs
// never affect the chat's session binding; this only feeds the HTML
// report. Failures degrade to no transcripts.
func (w *Wrapper) subAgentTranscripts(ctx context.Context, wsID, parentSessionID string, turnMsgs []proto.Message) []SubAgentTranscript {
	if !turnHasSubAgentCall(turnMsgs) {
		return nil
	}
	sessions, err := w.client.ListSessions(ctx, wsID)
	if err != nil {
		return nil
	}
	since := firstTurnCreatedAt(turnMsgs)
	var children []proto.Session
	for _, s := range sessions {
		if s.ParentSessionID != parentSessionID {
			continue
		}
		if since > 0 && s.CreatedAt > 0 && s.CreatedAt < since-1 {
			continue
		}
		children = append(children, s)
	}
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].CreatedAt != children[j].CreatedAt {
			return children[i].CreatedAt < children[j].CreatedAt
		}
		return children[i].ID < children[j].ID
	})
	var subs []SubAgentTranscript
	for _, c := range children {
		msgs, err := w.client.ListMessages(ctx, wsID, c.ID)
		if err != nil || len(msgs) == 0 {
			continue
		}
		subs = append(subs, SubAgentTranscript{SessionID: c.ID, Messages: msgs})
	}
	return subs
}

// turnSummary builds the one-line text notice sent after the HTML
// report.
func turnSummary(msgs []proto.Message, rc proto.RunComplete) string {
	var toolCalls int
	for i := range msgs {
		for _, tc := range msgs[i].ToolCalls() {
			if tc.Name != "" {
				toolCalls++
			}
		}
	}
	text := summaryReplyText(rc.Text)
	status := "完成"
	if rc.Cancelled {
		status = "已取消"
	}
	return fmt.Sprintf("✅ 本轮%s：%d 条消息，%d 次工具调用。详见 HTML 文件。%s",
		status, len(msgs), toolCalls, text)
}

// summaryReplyText extracts the first line of the final assistant
// text for the summary notice.
func summaryReplyText(s string) string {
	if s == "" {
		return ""
	}
	return "最后回复: " + firstLine(s)
}

func firstLine(s string) string {
	if line, _, ok := strings.Cut(s, "\n"); ok {
		return line
	}
	return s
}
