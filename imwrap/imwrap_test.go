package imwrap

import (
	"context"
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
	"github.com/stretchr/testify/require"
)

func startWrapper(t *testing.T) (*Wrapper, *fakeServer, *fakeAdapter) {
	t.Helper()
	f := newFakeServer(t)
	ad := &fakeAdapter{}
	w, err := New(Config{Client: f.client(t), Adapter: ad})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(w.Stop)
	return w, f, ad
}

// sendPrompt submits an agent prompt and waits until the server
// accepted it, returning the recorded AgentMessage.
func sendPrompt(t *testing.T, w *Wrapper, f *fakeServer, prompt string) proto.AgentMessage {
	t.Helper()
	before := len(f.agentMessages())
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "chat1", Text: prompt}))
	waitFor(t, "agent message to be sent", func() bool {
		return len(f.agentMessages()) > before
	})
	msgs := f.agentMessages()
	got := msgs[len(msgs)-1]
	require.Equal(t, prompt, got.msg.Prompt)
	return got.msg
}

func TestAgentTurnSendsHTMLReport(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "list the files")
	sid := sent.SessionID

	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "u1", Role: proto.User, SessionID: sid,
		Parts: []proto.ContentPart{proto.TextContent{Text: "list the files"}},
	})
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "a1", Role: proto.Assistant, SessionID: sid,
		Parts: []proto.ContentPart{
			proto.ReasoningContent{Thinking: "looking around"},
			proto.TextContent{Text: "here is the **answer**"},
		},
	})
	f.pushMessage(pubsub.UpdatedEvent, proto.Message{
		ID: "a1", Role: proto.Assistant, SessionID: sid,
		Parts: []proto.ContentPart{
			proto.ReasoningContent{Thinking: "looking around"},
			proto.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"ls"}`},
			proto.TextContent{Text: "here is the **answer**"},
			proto.Finish{Reason: proto.FinishReasonEndTurn},
		},
	})
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sid, RunID: sent.RunID, MessageID: "a1", Text: "here is the **answer**",
	}})

	waitFor(t, "HTML report file", func() bool {
		_, files := ad.snapshot()
		for _, file := range files {
			if strings.Contains(file.content, "here is the <strong>answer</strong>") &&
				strings.Contains(file.content, "🔧 bash") &&
				strings.Contains(file.content, "💭 Thinking") &&
				strings.Contains(file.content, "prompt: list the files") {
				return true
			}
		}
		return false
	})
	waitFor(t, "turn summary text", func() bool {
		return len(ad.findTexts(t, "本轮完成")) > 0
	})
}

func TestRunCompleteErrorNotifiesChat(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "do work")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID, Error: "model exploded",
	}})

	waitFor(t, "error notice", func() bool {
		return len(ad.findTexts(t, "本轮执行出错: model exploded")) > 0
	})
	waitFor(t, "HTML report file", func() bool {
		_, files := ad.snapshot()
		return len(files) > 0 && strings.Contains(files[0].content, "run error: model exploded")
	})
}

func TestAutoGrantPermission(t *testing.T) {
	t.Parallel()
	w, f, _ := startWrapper(t)

	sent := sendPrompt(t, w, f, "run something")
	f.pushSSE(pubsub.PayloadTypePermissionRequest, pubsub.Event[proto.PermissionRequest]{Payload: proto.PermissionRequest{
		ID: "p1", SessionID: sent.SessionID, ToolCallID: "tc1", ToolName: "bash",
		Action: "exec", Params: map[string]any{"command": "ls"},
	}})

	waitFor(t, "auto grant", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.grants) == 1 && f.grants[0].Action == proto.PermissionAllow
	})
}

func TestQuestionFlowSendsContextAndAcceptsAnswer(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "set up the db")
	sid := sent.SessionID
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "u1", Role: proto.User, SessionID: sid,
		Parts: []proto.ContentPart{proto.TextContent{Text: "set up the db"}},
	})
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "a1", Role: proto.Assistant, SessionID: sid,
		Parts: []proto.ContentPart{proto.TextContent{Text: "working on it"}},
	})

	f.pushSSE(pubsub.PayloadTypeQuestionRequest, pubsub.Event[proto.QuestionRequest]{Payload: proto.QuestionRequest{
		ID: "b1", SessionID: sid, ToolCallID: "tc9",
		Questions: []proto.QuestionItem{{
			ID: "q1", Type: "single_choice", Question: "Which database?",
			Choices: []proto.QuestionChoice{
				{ID: "pg", Label: "PostgreSQL"},
				{ID: "mongo", Label: "MongoDB"},
			},
		}},
	}})

	waitFor(t, "question context file", func() bool {
		_, files := ad.snapshot()
		return len(files) == 1 && strings.Contains(files[0].content, "working on it") &&
			strings.Contains(files[0].content, "进行中")
	})
	waitFor(t, "question text", func() bool {
		return len(ad.findTexts(t, "❓")) > 0 && len(ad.findTexts(t, "Which database?")) > 0
	})

	// The next free-text message answers the question instead of
	// starting a new turn.
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "chat1", Text: "2"}))
	waitFor(t, "answer submitted", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.answers) != 1 {
			return false
		}
		require.Equal(t, "b1", f.answers[0].BatchRequestID)
		require.Len(t, f.answers[0].Responses, 1)
		require.Equal(t, []string{"mongo"}, f.answers[0].Responses[0].SelectedIDs)
		return true
	})
	waitFor(t, "answer ack", func() bool {
		return len(ad.findTexts(t, "已提交回答")) > 0
	})
	f.mu.Lock()
	require.Len(t, f.agentMsgs, 1)
	f.mu.Unlock()

	// Completing the run still produces the final report.
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sid, RunID: sent.RunID, Text: "done",
	}})
	waitFor(t, "final report", func() bool {
		_, files := ad.snapshot()
		return len(files) == 2 && strings.Contains(files[1].content, "Crush 回复")
	})
}

func TestPromptsQueueWhileBusy(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	first := sendPrompt(t, w, f, "first prompt")
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "chat1", Text: "second prompt"}))
	waitFor(t, "queued notice", func() bool {
		return len(ad.findTexts(t, "已加入队列")) > 0
	})

	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: first.SessionID, RunID: first.RunID,
	}})
	waitFor(t, "queued prompt started", func() bool {
		msgs := f.agentMessages()
		return len(msgs) == 2 && msgs[1].msg.Prompt == "second prompt"
	})
}

func TestBuiltinHelpAndUnknownCommand(t *testing.T) {
	t.Parallel()
	w, _, ad := startWrapper(t)

	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", Text: "/help"}))
	texts, _ := ad.snapshot()
	require.Len(t, texts, 1)
	for _, name := range []string{"help", "sessions", "switch", "new", "export", "status", "cancel"} {
		require.Contains(t, texts[0], "/"+name)
	}

	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", Text: "/nosuch"}))
	texts, _ = ad.snapshot()
	require.Len(t, texts, 2)
	require.Contains(t, texts[1], "未知命令 /nosuch")
}

func TestSessionLifecycleCommands(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	// /new creates and switches.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new project alpha"}))
	texts, _ := ad.snapshot()
	require.Len(t, texts, 1)
	require.Contains(t, texts[0], "已创建并切换到新会话 sess-1")

	// A second session for switching tests.
	f.mu.Lock()
	f.sessions["ws1"]["sess-2"] = &proto.Session{ID: "sess-2", Title: "older", MessageCount: 7, UpdatedAt: 2}
	f.sessions["ws1"]["sess-1"].UpdatedAt = 5
	f.mu.Unlock()

	// /sessions lists both, current one marked.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/sessions"}))
	texts, _ = ad.snapshot()
	require.Len(t, texts, 2)
	require.Contains(t, texts[1], "sess-1")
	require.Contains(t, texts[1], "sess-2")
	require.Contains(t, texts[1], "▶")

	// /switch by index (most recently updated first: sess-1, sess-2).
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/switch 2"}))
	texts, _ = ad.snapshot()
	require.Len(t, texts, 3)
	require.Contains(t, texts[2], "已切换到会话 sess-2")

	// /switch by ID prefix, ambiguous.
	f.mu.Lock()
	f.sessions["ws1"]["sess-21"] = &proto.Session{ID: "sess-21", Title: "twin", UpdatedAt: 1}
	f.mu.Unlock()
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/switch sess-2"}))
	texts, _ = ad.snapshot()
	require.Len(t, texts, 4)
	require.Contains(t, texts[3], "匹配多个会话")

	// /status reflects the binding.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/status"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[4], "当前会话: sess-2")
}

func TestExportCommandSendsSessionHTML(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new"}))
	f.setMessages("ws1", "sess-1", []proto.Message{
		{ID: "u1", Role: proto.User, Parts: []proto.ContentPart{proto.TextContent{Text: "hi"}}},
		{ID: "a1", Role: proto.Assistant, Parts: []proto.ContentPart{proto.TextContent{Text: "hello there"}}},
	})

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/export"}))
	_, files := ad.snapshot()
	require.Len(t, files, 1)
	require.Contains(t, files[0].filename, "crush-session-sess-1-export-")
	require.Contains(t, files[0].content, "hello there")
	require.Contains(t, files[0].content, "会话导出")
	texts, _ := ad.snapshot()
	require.Len(t, texts, 2)
	require.Contains(t, texts[1], "已导出会话 sess-1")
}

func TestCancelCommand(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	sent := sendPrompt(t, w, f, "long task")
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/cancel"}))
	waitFor(t, "cancel notice", func() bool {
		return len(ad.findTexts(t, "已请求取消当前任务")) > 0
	})
	f.mu.Lock()
	require.Equal(t, 1, f.agentCancel)
	f.mu.Unlock()

	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID, Cancelled: true,
	}})
	waitFor(t, "cancelled summary", func() bool {
		return len(ad.findTexts(t, "本轮已取消")) > 0
	})
}

func TestCancelQuestionCommand(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "ask me")
	f.pushSSE(pubsub.PayloadTypeQuestionRequest, pubsub.Event[proto.QuestionRequest]{Payload: proto.QuestionRequest{
		ID: "b1", SessionID: sent.SessionID,
		Questions: []proto.QuestionItem{{ID: "q1", Type: "yes_no", Question: "Proceed?"}},
	}})
	waitFor(t, "question surfaced", func() bool {
		return len(ad.findTexts(t, "❓")) > 0
	})

	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "chat1", Text: "/cancel"}))
	waitFor(t, "question cancelled", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cancels == 1
	})
	require.Len(t, ad.findTexts(t, "已取消当前问题"), 1)
}

func TestCustomCommandRegistrationAndDispatch(t *testing.T) {
	t.Parallel()
	w, _, ad := startWrapper(t)
	ctx := context.Background()

	require.NoError(t, w.RegisterCommand("deploy", "run the deploy pipeline", func(ctx context.Context, c CommandContext) error {
		if err := c.Reply(ctx, "deploying "+c.ArgText); err != nil {
			return err
		}
		return c.ReplyFile(ctx, "log.txt", []byte("log body"))
	}))
	require.ErrorIs(t, w.RegisterCommand("deploy", "dup", func(context.Context, CommandContext) error { return nil }), ErrCommandExists)
	require.ErrorIs(t, w.RegisterCommand("export", "hijack", func(context.Context, CommandContext) error { return nil }), ErrCommandExists)
	require.Error(t, w.RegisterCommand("", "no name", func(context.Context, CommandContext) error { return nil }))

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/deploy   prod fast"}))
	texts, files := ad.snapshot()
	require.Len(t, texts, 1)
	require.Equal(t, "c|deploying prod fast", texts[0])
	require.Len(t, files, 1)
	require.Equal(t, "log body", files[0].content)

	require.NoError(t, w.UnregisterCommand("deploy"))
	require.Error(t, w.UnregisterCommand("export")) // builtin
	require.Error(t, w.UnregisterCommand("deploy")) // already gone

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/deploy again"}))
	texts, _ = ad.snapshot()
	require.Len(t, texts, 2)
	require.Contains(t, texts[1], "未知命令 /deploy")
}

func TestCommandsListing(t *testing.T) {
	t.Parallel()
	w, _, _ := startWrapper(t)

	require.NoError(t, w.RegisterCommand("zzz", "last", func(context.Context, CommandContext) error { return nil }))
	require.NoError(t, w.RegisterCommand("aaa", "first", func(context.Context, CommandContext) error { return nil }))

	cmds := w.Commands()
	require.GreaterOrEqual(t, len(cmds), 9)
	// Builtins first, customs alphabetical.
	var customs []CommandInfo
	for i, c := range cmds {
		if !c.Builtin {
			customs = append(customs, c)
			require.False(t, cmds[i-1].Builtin == false && cmds[i-1].Name > c.Name)
		}
	}
	require.Len(t, customs, 2)
	require.Equal(t, "aaa", customs[0].Name)
	require.Equal(t, "zzz", customs[1].Name)
}

func TestEmptyMessageIgnored(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", Text: "   "}))
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", Text: "/"}))
	_, files := ad.snapshot()
	require.Empty(t, files)
	require.Empty(t, f.agentMessages())
}

func TestAgentPromptCreatesSessionOnce(t *testing.T) {
	t.Parallel()
	w, f, _ := startWrapper(t)

	sent := sendPrompt(t, w, f, "hello one")
	require.Equal(t, "sess-1", sent.SessionID)
	require.NotEmpty(t, sent.RunID)

	// Run 1 completes; a new prompt must reuse the same session.
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID,
	}})
	sent2 := sendPrompt(t, w, f, "hello two")
	require.Equal(t, "sess-1", sent2.SessionID)
	require.NotEqual(t, sent.RunID, sent2.RunID)
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	_, err := New(Config{Adapter: &fakeAdapter{}})
	require.ErrorContains(t, err, "Config.Client")
	_, err = New(Config{Client: f.client(t)})
	require.ErrorContains(t, err, "Config.Adapter")
}

func TestStartCreatesWorkspaceWithYOLO(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad := &fakeAdapter{}
	w, err := New(Config{Client: f.client(t), Adapter: ad})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(w.Stop)

	require.Equal(t, "ws1", w.WorkspaceID())
	f.mu.Lock()
	require.True(t, f.workspaces["ws1"].YOLO)
	f.mu.Unlock()
}
