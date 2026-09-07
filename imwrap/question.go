package imwrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/roberChen/crush-sdk/proto"
)

// onQuestionRequest surfaces a question batch to the chat waiting on
// that session: it sends the turn-so-far HTML file, then a text
// message with the questions, and records the pending question so the
// next non-command reply is treated as the answer.
func (w *Wrapper) onQuestionRequest(wsID string, req proto.QuestionRequest) {
	w.mu.Lock()
	chatID := w.chatForSession(req.SessionID)
	if chatID == "" {
		w.mu.Unlock()
		slog.Debug("imwrap: question for untracked session", "session", req.SessionID)
		return
	}
	st := w.state(chatID)
	st.pendingQ = &pendingQuestion{req: req, chatID: chatID, wsID: wsID}
	var progress []proto.Message
	prompt := ""
	if t, ok := w.turns[req.SessionID]; ok {
		progress = t.snapshot()
		prompt = t.prompt
	}
	w.mu.Unlock()

	w.wg.Go(func() {
		ctx, err := w.runCtx()
		if err != nil {
			return
		}
		if len(progress) > 0 {
			name := fmt.Sprintf("crush-question-%s.html", timestampSlug(now()))
			if err := w.adapter.SendFile(ctx, chatID, name, RenderTurnHTML(prompt, progress, nil)); err != nil {
				w.notifyError(chatID, "failed to send question context file", err)
			}
		}
		if err := w.adapter.SendText(ctx, chatID, FormatQuestionText(req)); err != nil {
			w.notifyError(chatID, "failed to send question", err)
		}
	})
}

// onQuestionNotification clears a pending question that was resolved
// elsewhere (another client answered it first).
func (w *Wrapper) onQuestionNotification(n proto.QuestionNotification) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, st := range w.chats {
		if st.pendingQ != nil && st.pendingQ.req.ID == n.BatchID {
			st.pendingQ = nil
		}
	}
}

// chatForSession finds the chat to surface session events for: the
// chat with an in-flight run on the session, else any chat bound to
// it. Caller must hold Wrapper.mu.
func (w *Wrapper) chatForSession(sessionID string) string {
	for _, run := range w.runs {
		if run.attached && run.sessionID == sessionID {
			return run.chatID
		}
	}
	for id, st := range w.chats {
		if st.busy && st.activeSession == sessionID {
			return id
		}
	}
	for id, st := range w.chats {
		if st.sessionID == sessionID {
			return id
		}
	}
	return ""
}

// pendingQuestionFor returns the pending question for a chat without
// clearing it, so a failed answer parse can keep it pending.
func (w *Wrapper) pendingQuestionFor(chatID string) *pendingQuestion {
	w.mu.Lock()
	defer w.mu.Unlock()
	if st, ok := w.chats[chatID]; ok {
		return st.pendingQ
	}
	return nil
}

// clearPendingQuestion drops the pending question for a chat.
func (w *Wrapper) clearPendingQuestion(chatID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if st, ok := w.chats[chatID]; ok {
		st.pendingQ = nil
	}
}

// answerQuestion parses the user's reply against the pending
// question batch and submits it. When the reply does not parse the
// question stays pending and a help text is sent instead.
func (w *Wrapper) answerQuestion(ctx context.Context, chatID string, q *pendingQuestion, reply string) error {
	answer, err := BuildQuestionAnswer(q.req, reply)
	if err != nil {
		help := "无法解析回答: " + err.Error() + "\n" + FormatQuestionText(q.req)
		return w.adapter.SendText(ctx, chatID, help)
	}
	if _, err := w.client.AnswerQuestionBatch(ctx, q.wsID, answer); err != nil {
		return fmt.Errorf("failed to submit question answer: %w", err)
	}
	w.clearPendingQuestion(chatID)
	return w.adapter.SendText(ctx, chatID, "✔ 已提交回答，继续执行…")
}

// FormatQuestionText renders a question batch as an IM-friendly text
// message: numbered questions, numbered choices, and a hint on how to
// reply.
func FormatQuestionText(req proto.QuestionRequest) string {
	var b strings.Builder
	b.WriteString("❓ agent 需要你的回答（直接回复本消息；/cancel 取消）\n")
	for i, q := range req.Questions {
		if len(req.Questions) > 1 {
			fmt.Fprintf(&b, "\n%d) ", i+1)
		} else {
			b.WriteString("\n")
		}
		b.WriteString(questionLine(q))
	}
	if len(req.Questions) > 1 {
		b.WriteString("\n（多个问题请逐行回复，格式：1: 答案）")
	}
	return b.String()
}

func questionLine(q proto.QuestionItem) string {
	var b strings.Builder
	if q.Label != "" {
		fmt.Fprintf(&b, "[%s] ", q.Label)
	}
	b.WriteString(q.Question)
	if q.Description != "" {
		fmt.Fprintf(&b, "\n   %s", q.Description)
	}
	switch {
	case isYesNo(q):
		b.WriteString("\n   回复 yes 或 no")
	case len(q.Choices) > 0:
		marker := "回复序号"
		if isMultiChoice(q) {
			marker = "回复序号（可用逗号分隔多选）"
		}
		fmt.Fprintf(&b, "\n   %s:", marker)
		for i, c := range q.Choices {
			if c.Description != "" {
				fmt.Fprintf(&b, "\n   %d. %s - %s", i+1, c.Label, c.Description)
			} else {
				fmt.Fprintf(&b, "\n   %d. %s", i+1, c.Label)
			}
		}
	default:
		b.WriteString("\n   直接回复文本")
	}
	return b.String()
}

// BuildQuestionAnswer parses an IM reply into a QuestionAnswer for
// the given batch. For single-question batches the whole reply is the
// answer; for multi-question batches lines of the form "N: answer"
// (or "N) answer") distribute answers by index, and any unmatched
// question receives the whole (trimmed) reply.
func BuildQuestionAnswer(req proto.QuestionRequest, reply string) (proto.QuestionAnswer, error) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return proto.QuestionAnswer{}, errors.New("回复为空")
	}

	perQuestion := make([]string, len(req.Questions))
	for i := range perQuestion {
		perQuestion[i] = reply
	}
	if len(req.Questions) > 1 {
		general := []string{}
		for line := range strings.SplitSeq(reply, "\n") {
			n, text, ok := splitNumberedLine(line)
			if ok && n >= 1 && n <= len(req.Questions) {
				perQuestion[n-1] = text
				continue
			}
			general = append(general, strings.TrimSpace(line))
		}
		joined := strings.TrimSpace(strings.Join(general, "\n"))
		if joined != "" {
			for i := range perQuestion {
				perQuestion[i] = joined
			}
		}
	}

	responses := make([]proto.QuestionResponse, 0, len(req.Questions))
	for i, q := range req.Questions {
		resp, err := parseAnswerFor(q, perQuestion[i])
		if err != nil {
			return proto.QuestionAnswer{}, fmt.Errorf("问题 %d: %w", i+1, err)
		}
		resp.QuestionID = q.ID
		responses = append(responses, resp)
	}
	return proto.QuestionAnswer{BatchRequestID: req.ID, Responses: responses}, nil
}

// splitNumberedLine recognizes leading "N:" / "N)" / "N." / "N、" markers.
func splitNumberedLine(line string) (n int, text string, ok bool) {
	line = strings.TrimSpace(line)
	for _, sep := range []string{":", ")", ".", "、", "："} {
		if i := strings.Index(line, sep); i > 0 {
			if v, err := strconv.Atoi(strings.TrimSpace(line[:i])); err == nil {
				return v, strings.TrimSpace(line[i+len(sep):]), true
			}
		}
	}
	return 0, "", false
}

// parseAnswerFor parses one reply against one question item.
func parseAnswerFor(q proto.QuestionItem, text string) (proto.QuestionResponse, error) {
	switch {
	case isYesNo(q):
		yes, ok := parseBool(text)
		if !ok {
			return proto.QuestionResponse{}, fmt.Errorf("无法识别 %q, 请回复 yes 或 no", truncate(text, 40))
		}
		return proto.QuestionResponse{Yes: &yes}, nil

	case len(q.Choices) > 0:
		tokens := splitChoiceTokens(text)
		if len(tokens) == 0 {
			return proto.QuestionResponse{}, errors.New("请回复选项序号或选项文本")
		}
		var ids []string
		for _, tok := range tokens {
			id, err := matchChoice(q.Choices, tok)
			if err != nil {
				return proto.QuestionResponse{}, err
			}
			if !containsID(ids, id) {
				ids = append(ids, id)
			}
			if !isMultiChoice(q) {
				break
			}
		}
		return proto.QuestionResponse{SelectedIDs: ids}, nil

	default:
		return proto.QuestionResponse{FillInText: strings.TrimSpace(text)}, nil
	}
}

func splitChoiceTokens(text string) []string {
	r := strings.NewReplacer(",", " ", "，", " ", ";", " ", "；", " ", "\n", " ")
	return strings.Fields(r.Replace(strings.TrimSpace(text)))
}

// matchChoice resolves one reply token to a choice: a 1-based index,
// or a (case-insensitive) exact-then-prefix match on label or ID.
func matchChoice(choices []proto.QuestionChoice, tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if n, err := strconv.Atoi(tok); err == nil {
		if n >= 1 && n <= len(choices) {
			return choices[n-1].ID, nil
		}
		return "", fmt.Errorf("序号 %d 超出范围 (1-%d)", n, len(choices))
	}
	lower := strings.ToLower(tok)
	for _, c := range choices {
		if strings.ToLower(c.Label) == lower || strings.ToLower(c.ID) == lower {
			return c.ID, nil
		}
	}
	var prefixed []string
	for _, c := range choices {
		if strings.HasPrefix(strings.ToLower(c.Label), lower) || strings.HasPrefix(strings.ToLower(c.ID), lower) {
			prefixed = append(prefixed, c.ID)
		}
	}
	switch len(prefixed) {
	case 1:
		return prefixed[0], nil
	case 0:
		return "", fmt.Errorf("没有匹配 %q 的选项", truncate(tok, 40))
	default:
		return "", fmt.Errorf("%q 匹配多个选项，请回复更完整的文本或使用序号", truncate(tok, 40))
	}
}

func containsID(ids []string, id string) bool {
	return slices.Contains(ids, id)
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "1", "是", "好", "对", "嗯", "ok", "okay":
		return true, true
	case "n", "no", "false", "0", "否", "不", "错":
		return false, true
	}
	return false, false
}

// Question item types are free-form strings on the wire; classify
// defensively so unknown variants fall through to sensible behavior.
func isYesNo(q proto.QuestionItem) bool {
	return strings.Contains(strings.ToLower(q.Type), "yes")
}

func isMultiChoice(q proto.QuestionItem) bool {
	return strings.Contains(strings.ToLower(q.Type), "multi")
}
