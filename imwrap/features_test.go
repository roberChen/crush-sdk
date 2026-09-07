package imwrap

import (
	"context"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/roberChen/crush-sdk/config"
	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
	"github.com/stretchr/testify/require"
)

func TestImmediateAckOnPrompt(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sendPrompt(t, w, f, "hello")
	waitFor(t, "immediate acknowledgement", func() bool {
		return len(ad.findTexts(t, "🤖 已收到，正在处理")) > 0
	})
}

func seedModels(f *fakeServer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = ModelsConfig{
		Current: config.SelectedModel{Provider: "anthropic", Model: "claude-3"},
		Providers: map[string][]catwalk.Model{
			"anthropic": {
				{ID: "claude-3", Name: "Claude 3", ContextWindow: 200_000},
				{ID: "claude-4", Name: "Claude 4", ContextWindow: 1_000_000},
			},
			"openai": {
				{ID: "gpt-5", Name: "GPT-5", ContextWindow: 400_000},
			},
		},
	}
}

func TestListModelsAndSetModel(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	seedModels(f)
	ctx := context.Background()

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/models"}))
	texts, _ := ad.snapshot()
	require.Len(t, texts, 1)
	require.Contains(t, texts[0], "anthropic")
	require.Contains(t, texts[0], "claude-3")
	require.Contains(t, texts[0], "claude-4")
	require.Contains(t, texts[0], "openai")
	require.Contains(t, texts[0], "gpt-5")
	require.Contains(t, texts[0], "▶ claude-3")
	require.Contains(t, texts[0], "ctx 200k")

	// /model with no args shows the current one.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/model"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[1], "当前模型: anthropic/claude-3")

	// /model sets via provider/model.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/model openai/gpt-5"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[2], "✅ 已切换模型: openai/gpt-5")
	f.mu.Lock()
	require.Len(t, f.modelSets, 1)
	require.Equal(t, config.ScopeWorkspace, f.modelSets[0].body.Scope)
	require.Equal(t, config.SelectedModelTypeLarge, f.modelSets[0].body.ModelType)
	require.Equal(t, config.SelectedModel{Provider: "openai", Model: "gpt-5"}, f.modelSets[0].body.Model)
	f.mu.Unlock()

	// Bare model id resolves to its unique provider.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/model claude-4"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[3], "✅ 已切换模型: anthropic/claude-4")

	// Unknown model reports a helpful error.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/model nope"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[4], "设置模型失败")
	require.Contains(t, texts[4], "no model matches")
}

func TestAskOnceReportsSessionIDAndRestoresModel(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	seedModels(f)
	ctx := context.Background()

	// Establish the chat's main session first.
	main := sendPrompt(t, w, f, "main prompt")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: main.SessionID, RunID: main.RunID,
	}})
	waitFor(t, "main turn finalized", func() bool {
		return len(ad.findTexts(t, "本轮完成")) > 0
	})

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/ask -m openai/gpt-5 -t quick one-shot question"}))
	waitFor(t, "ack", func() bool { return len(ad.findTexts(t, "一次性对话已启动")) > 0 })

	// The model override is applied before the prompt is sent.
	f.mu.Lock()
	sets := append([]recordedModelSet{}, f.modelSets...)
	f.mu.Unlock()
	require.Len(t, sets, 1)
	require.Equal(t, config.SelectedModel{Provider: "openai", Model: "gpt-5"}, sets[0].body.Model)

	// The one-shot runs in its own session.
	var oneshot recordedAgent
	waitFor(t, "one-shot prompt sent", func() bool {
		msgs := f.agentMessages()
		for _, m := range msgs {
			if m.msg.Prompt == "one-shot question" {
				oneshot = m
				return true
			}
		}
		return false
	})
	require.NotEqual(t, main.SessionID, oneshot.msg.SessionID)

	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: oneshot.msg.SessionID, RunID: oneshot.msg.RunID, Text: "oneshot done",
	}})
	waitFor(t, "session id reported", func() bool {
		return len(ad.findTexts(t, "session id: "+oneshot.msg.SessionID)) > 0
	})

	// The previous default model is restored afterwards.
	waitFor(t, "model restored", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.modelSets) == 2 &&
			f.modelSets[1].body.Model.Provider == "anthropic" &&
			f.modelSets[1].body.Model.Model == "claude-3"
	})

	// The chat binding is untouched: the next prompt goes to the main
	// session again.
	next := sendPrompt(t, w, f, "back to main")
	require.Equal(t, main.SessionID, next.SessionID)
}

func TestAskOnceWithoutModel(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	main := sendPrompt(t, w, f, "main prompt")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: main.SessionID, RunID: main.RunID,
	}})

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/ask just this once"}))
	var oneshot recordedAgent
	waitFor(t, "one-shot sent", func() bool {
		for _, m := range f.agentMessages() {
			if m.msg.Prompt == "just this once" {
				oneshot = m
				return true
			}
		}
		return false
	})
	f.mu.Lock()
	require.Empty(t, f.modelSets)
	f.mu.Unlock()

	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: oneshot.msg.SessionID, RunID: oneshot.msg.RunID,
	}})
	waitFor(t, "session id reported", func() bool {
		return len(ad.findTexts(t, "session id: "+oneshot.msg.SessionID)) > 0
	})
}

func TestAskInSessionKeepsChatBinding(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	// Main session for chat1.
	main := sendPrompt(t, w, f, "main prompt")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: main.SessionID, RunID: main.RunID,
	}})

	// Another session in the same workspace.
	f.mu.Lock()
	f.sessions["ws1"]["sess-other"] = &proto.Session{ID: "sess-other", Title: "other", UpdatedAt: 9}
	f.mu.Unlock()

	// /say targets it by prefix (shortID renders "sess-oth").
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/say sess-other one-off question"}))
	waitFor(t, "say ack", func() bool {
		return len(ad.findTexts(t, "已发送到会话 sess-oth")) > 0
	})
	var said recordedAgent
	waitFor(t, "say prompt sent", func() bool {
		for _, m := range f.agentMessages() {
			if m.msg.Prompt == "one-off question" {
				said = m
				return true
			}
		}
		return false
	})
	require.Equal(t, "sess-other", said.msg.SessionID)

	// The chat is not busy: a normal prompt runs immediately on the
	// bound session, concurrently with the one-off.
	next := sendPrompt(t, w, f, "still my session")
	require.Equal(t, main.SessionID, next.SessionID)

	// Completing the one-off reports to the chat without touching it.
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "said-1", Role: proto.Assistant, SessionID: "sess-other",
		Parts: []proto.ContentPart{proto.TextContent{Text: "said done"}},
	})
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "sess-other", RunID: said.msg.RunID, Text: "said done",
	}})
	waitFor(t, "one-off report", func() bool {
		_, files := ad.snapshot()
		for _, file := range files {
			if strings.Contains(file.content, "said done") {
				return true
			}
		}
		return false
	})

	// Unknown session id is reported as an error notice.
	err := w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/say missing-session hi"})
	require.ErrorContains(t, err, "not found in any known workspace")
	waitFor(t, "not found notice", func() bool {
		return len(ad.findTexts(t, "not found")) > 0
	})
}

func TestCreateSessionWithDirectoryAndInfo(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()
	otherDir := t.TempDir()

	// /new with a directory creates the session in that directory's
	// workspace.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d " + otherDir + " side project"}))
	texts, _ := ad.snapshot()
	require.Len(t, texts, 1)
	require.Contains(t, texts[0], "side project")
	require.Contains(t, texts[0], otherDir)

	f.mu.Lock()
	require.Len(t, f.workspaces, 2)
	var otherWS string
	for id, ws := range f.workspaces {
		if id != "ws1" {
			require.Equal(t, otherDir, ws.Path)
			otherWS = id
		}
	}
	// /info reads skills from the session's workspace.
	f.workspaces[otherWS].Skills = []proto.SkillState{
		{Name: "crush-config", Path: "/skills/crush-config", State: proto.SkillStateNormal},
		{Name: "broken", Path: "/skills/broken", State: proto.SkillStateError, Error: "parse failed"},
	}
	f.mu.Unlock()

	// /info shows directory, skills, tools, and watermark.
	f.setSession(otherWS, "sess-1", func(s *proto.Session) {
		s.PromptTokens = 50_000
		s.CompletionTokens = 3_000
		s.MessageCount = 5
	})
	seedModels(f)
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/info"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[1], "目录: "+otherDir)
	require.Contains(t, texts[1], "上下文水位: 50000/200000 tokens (25.0%)")
	require.Contains(t, texts[1], "技能: 2 个")
	require.Contains(t, texts[1], "✓ crush-config")
	require.Contains(t, texts[1], "✗ broken (parse failed)")
	require.Contains(t, texts[1], "工具: ")
	require.Contains(t, texts[1], "bash")
	require.Contains(t, texts[1], "模型: anthropic/claude-3")

	// Prompts now run in the other workspace (same chat "c").
	before := len(f.agentMessages())
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "work over there"}))
	waitFor(t, "prompt sent", func() bool { return len(f.agentMessages()) > before })
	f.mu.Lock()
	last := f.agentMsgs[len(f.agentMsgs)-1]
	f.mu.Unlock()
	require.Equal(t, otherWS, last.wsID)
	require.Equal(t, "sess-1", last.msg.SessionID)

	// /sessions lists sessions from both workspaces with their dirs.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/sessions"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[3], "@ "+otherDir)

	// /ask -d runs a one-shot in the default workspace's directory.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/ask -d " + t.TempDir() + " quick"}))
	waitFor(t, "third workspace created", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.workspaces) == 3
	})
}

func TestParseFlagArgs(t *testing.T) {
	t.Parallel()

	flags, rest := parseFlagArgs([]string{"-m", "openai/gpt-5", "-d", "/tmp", "--title", "hi", "do", "the", "thing"})
	require.Equal(t, map[string]string{"m": "openai/gpt-5", "d": "/tmp", "title": "hi"}, flags)
	require.Equal(t, []string{"do", "the", "thing"}, rest)

	flags, rest = parseFlagArgs([]string{"plain", "-m", "x"})
	require.Empty(t, flags)
	require.Equal(t, []string{"plain", "-m", "x"}, rest)

	flags, rest = parseFlagArgs(nil)
	require.Empty(t, flags)
	require.Empty(t, rest)

	flags, rest = parseFlagArgs([]string{"-d"})
	require.Equal(t, map[string]string{"d": ""}, flags)
	require.Empty(t, rest)
}

func TestSayUsageErrors(t *testing.T) {
	t.Parallel()
	w, _, ad := startWrapper(t)
	ctx := context.Background()

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/say"}))
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/say only-session"}))
	texts, _ := ad.snapshot()
	require.Len(t, texts, 2)
	require.Contains(t, texts[0], "用法: /say")
	require.Contains(t, texts[1], "用法: /say")
}

func TestHumanTokens(t *testing.T) {
	t.Parallel()
	require.Equal(t, "?", humanTokens(0))
	require.Equal(t, "128k", humanTokens(128_000))
	require.Equal(t, "1.0M", humanTokens(1_000_000))
	require.Equal(t, "1.2M", humanTokens(1_200_000))
}

func TestSubAgentToolCallRendersTranscriptAndKeepsBinding(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "delegate the scan")

	// The agent spawns a sub-agent session (task tool) and gets its
	// result.
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "a1", Role: proto.Assistant, SessionID: sent.SessionID, CreatedAt: 100,
		Parts: []proto.ContentPart{
			proto.ToolCall{
				ID:    "tc1",
				Name:  "task",
				Input: `{"description":"scan repo","prompt":"scan for TODOs"}`,
			},
		},
	})
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "t1", Role: proto.Tool, SessionID: sent.SessionID,
		Parts: []proto.ContentPart{proto.ToolResult{ToolCallID: "tc1", Name: "task", Content: "found 2 TODOs"}},
	})

	// The sub-agent session exists server-side with its own messages.
	f.mu.Lock()
	f.sessions["ws1"]["child-1"] = &proto.Session{
		ID: "child-1", ParentSessionID: sent.SessionID, Title: "task", CreatedAt: 101, UpdatedAt: 102,
	}
	f.messages["ws1"]["child-1"] = []proto.Message{
		{ID: "c-u", Role: proto.User, Parts: []proto.ContentPart{proto.TextContent{Text: "scan for TODOs"}}},
		{ID: "c-a", Role: proto.Assistant, Parts: []proto.ContentPart{
			proto.ToolCall{ID: "c-tc", Name: "bash", Input: `{"command":"grep -r TODO"}`},
		}},
		{ID: "c-f", Role: proto.Assistant, Parts: []proto.ContentPart{proto.TextContent{Text: "sub agent done"}}},
	}
	// An older child from a previous turn must be filtered out.
	f.sessions["ws1"]["child-old"] = &proto.Session{
		ID: "child-old", ParentSessionID: sent.SessionID, CreatedAt: 50, UpdatedAt: 51,
	}
	f.messages["ws1"]["child-old"] = []proto.Message{
		{ID: "old", Role: proto.User, Parts: []proto.ContentPart{proto.TextContent{Text: "stale transcript"}}},
	}
	f.mu.Unlock()

	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID, Text: "delegated",
	}})

	// The HTML report nests the sub-agent transcript.
	waitFor(t, "report with sub-agent transcript", func() bool {
		_, files := ad.snapshot()
		for _, file := range files {
			if strings.Contains(file.content, "🧒 子 agent 会话 child-1") &&
				strings.Contains(file.content, "grep -r TODO") &&
				strings.Contains(file.content, "sub agent done") &&
				strings.Contains(file.content, "found 2 TODOs") &&
				strings.Contains(file.content, "输入") {
				return true
			}
		}
		return false
	})
	_, files := ad.snapshot()
	for _, file := range files {
		require.NotContains(t, file.content, "stale transcript")
		require.NotContains(t, file.content, "child-old")
	}

	// The chat binding was NOT switched to the sub-agent session.
	next := sendPrompt(t, w, f, "back on the main session")
	require.Equal(t, sent.SessionID, next.SessionID)
}
