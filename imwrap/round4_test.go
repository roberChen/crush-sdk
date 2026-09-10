package imwrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
	"github.com/stretchr/testify/require"
)

func TestExportHTMLIncludesFooter(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new export-check"}))
	f.setMessages("ws1", "sess-1", []proto.Message{
		{ID: "u1", Role: proto.User, Parts: []proto.ContentPart{proto.TextContent{Text: "hi"}}},
	})
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/export"}))
	_, files := ad.snapshot()
	require.Len(t, files, 1)
	require.Contains(t, files[0].content, "会话导出")
	require.Contains(t, files[0].content, "session-footer")
	require.Contains(t, files[0].content, "目录")
	require.Contains(t, files[0].content, "模型")
}

// multiClientFixture binds chat1 to a session, then another "client"
// drives it (unknown RunID) while the bot queues a prompt.
func multiClientFixture(t *testing.T) (*Wrapper, *fakeServer, *fakeAdapter) {
	t.Helper()
	w, f, ad := startWrapper(t)
	sent := sendPrompt(t, w, f, "warm up")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID,
	}})
	waitFor(t, "first turn done", func() bool {
		return len(ad.findTexts(t, "本轮完成")) > 0
	})
	return w, f, ad
}

func TestExternalRunCompletionIsReported(t *testing.T) {
	t.Parallel()
	_, f, ad := multiClientFixture(t)

	// Another client drives the session: message events stream (the
	// eager collector picks them up) and a RunComplete with an
	// unknown RunID arrives.
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "x-u", Role: proto.User, SessionID: "sess-1",
		Parts: []proto.ContentPart{proto.TextContent{Text: "other client prompt"}},
	})
	f.pushMessage(pubsub.CreatedEvent, proto.Message{
		ID: "x-a", Role: proto.Assistant, SessionID: "sess-1",
		Parts: []proto.ContentPart{proto.TextContent{Text: "other client answer"}},
	})
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "sess-1", RunID: "external-run-id", Text: "other client answer",
	}})

	waitFor(t, "external turn report", func() bool {
		_, files := ad.snapshot()
		for _, file := range files {
			if strings.Contains(file.content, "other client answer") &&
				strings.Contains(file.content, "由其他客户端触发") {
				return true
			}
		}
		return false
	})
}

func TestQueueBehindOtherClientsRunAndDrain(t *testing.T) {
	t.Parallel()
	w, f, ad := multiClientFixture(t)
	ctx := context.Background()

	// The session is busy (driven by another client, per the server).
	f.setSession("ws1", "sess-1", func(s *proto.Session) { s.IsBusy = true })

	// The bot's prompt must queue with a clear notice.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "my follow-up"}))
	waitFor(t, "queue notice", func() bool {
		return len(ad.findTexts(t, "会话正忙（可能由其他客户端触发），已加入队列")) > 0
	})

	// /status shows the session-level busy.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/status"}))
	waitFor(t, "status shows busy", func() bool {
		return len(ad.findTexts(t, "会话状态: busy")) > 0
	})

	// The other client's run completes: the bot reports it and
	// starts its queued prompt.
	f.setSession("ws1", "sess-1", func(s *proto.Session) { s.IsBusy = false })
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "sess-1", RunID: "other-client-run", Text: "their answer",
	}})
	waitFor(t, "queued prompt started after external completion", func() bool {
		for _, m := range f.agentMessages() {
			if m.msg.Prompt == "my follow-up" {
				return true
			}
		}
		return false
	})
	waitFor(t, "external report sent", func() bool {
		return len(ad.findTexts(t, "本轮完成")) > 1
	})
}

func setupGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644))
	}
	write("a.txt", "one\n")
	run("add", ".")
	run("commit", "-m", "first commit")
	write("a.txt", "one\ntwo\n")
	write("b.txt", "new file\n")
	run("add", ".")
	run("commit", "-m", "second commit")
	return repo
}

func TestGitLogCommand(t *testing.T) {
	t.Parallel()
	repo := setupGitRepo(t)
	w, f, ad := startWrapper(t)
	ctx := context.Background()
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d " + repo + " gitlog"}))

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git log 2"}))
	_, files := ad.snapshot()
	require.Len(t, files, 1)
	require.Contains(t, files[0].filename, "crush-gitlog-")
	require.Contains(t, files[0].content, "second commit")
	require.Contains(t, files[0].content, "first commit")
	require.Contains(t, files[0].content, "合并视图")
	require.Contains(t, files[0].content, "session-footer")
	texts, _ := ad.snapshot()
	require.Contains(t, texts[1], "最近 2 个提交")
	_ = f
}

func TestGitBranchCheckoutPushPull(t *testing.T) {
	t.Parallel()
	repo := setupGitRepo(t)
	// A bare remote so push/pull work for real.
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(repo, "init", "--bare", origin)
	run(repo, "remote", "add", "origin", origin)
	run(repo, "push", "-u", "origin", "main")
	run(repo, "branch", "feature")
	run(repo, "push", "-u", "origin", "feature")

	w, _, ad := startWrapper(t)
	ctx := context.Background()
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d " + repo + " ops"}))

	// /git checkout without args lists branches.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git checkout"}))
	texts, _ := ad.snapshot()
	require.Contains(t, texts[1], "feature")
	require.Contains(t, texts[1], "main")

	// /git checkout switches branch.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git checkout feature"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[2], "✅ git checkout")

	// /git push succeeds against the bare remote.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git push origin feature"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[3], "✅ git push")

	// /git pull works on the synced repo.
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git pull"}))
	texts, _ = ad.snapshot()
	require.Contains(t, texts[4], "✅ git pull")
}

func TestGitSubcommandUsage(t *testing.T) {
	t.Parallel()
	w, _, ad := startWrapper(t)
	ctx := context.Background()

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git bogus"}))
	texts, _ := ad.snapshot()
	require.Contains(t, texts[0], "用法: /git")
}
