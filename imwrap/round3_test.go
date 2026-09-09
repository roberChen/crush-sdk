package imwrap

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
	"github.com/stretchr/testify/require"
)

func TestConsumedToolMessageLeavesNoEmptySection(t *testing.T) {
	t.Parallel()

	html := string(RenderTurnHTML("p", editTurnMessages(), nil))
	// Every tool result was rendered with its call; the bare tool
	// message must not leave an empty shell behind.
	require.NotContains(t, html, `class="msg role-tool"`)
	// ...and the results themselves are still present, inline.
	require.Contains(t, html, "📤 结果")
	require.Contains(t, html, ">ok</pre>")

	// A tool message whose result has no matching call still renders
	// standalone.
	orphan := append(editTurnMessages(), proto.Message{
		ID:   "t9",
		Role: proto.Tool,
		Parts: []proto.ContentPart{
			proto.ToolResult{ToolCallID: "tc-orphan", Name: "bash", Content: "orphan output"},
		},
	})
	html = string(RenderTurnHTML("p", orphan, nil))
	require.Contains(t, html, `class="msg role-tool"`)
	require.Contains(t, html, "orphan output")
}

func TestTurnHTMLCarriesSessionFooter(t *testing.T) {
	t.Parallel()

	footer := &SessionFooter{
		SessionID:        "sess-footer",
		Title:            "footer check",
		Dir:              "/repo/x",
		Model:            "anthropic/claude-3",
		ModelName:        "Claude 3",
		ContextWindow:    200_000,
		PromptTokens:     100_000,
		CompletionTokens: 20_000,
		MessageCount:     42,
		Cost:             1.25,
		GitBranch:        "main",
		GitDirty:         3,
		Queued:           1,
		GeneratedAt:      now(),
	}
	html := string(RenderTurnHTML("p", nil, nil, WithFooter(footer)))
	require.Contains(t, html, "footer check")
	require.Contains(t, html, "/repo/x")
	require.Contains(t, html, "anthropic/claude-3")
	require.Contains(t, html, "120k / 200k tokens（60.0%）")
	require.Contains(t, html, "main（3 处未提交）")
	require.Contains(t, html, "1 条待处理")
	require.Contains(t, html, "session-footer")
}

func TestTurnReportIncludesFooter(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sent := sendPrompt(t, w, f, "with footer")
	f.pushSSE(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sent.SessionID, RunID: sent.RunID,
	}})
	waitFor(t, "footer in report", func() bool {
		_, files := ad.snapshot()
		for _, file := range files {
			if strings.Contains(file.content, "session-footer") &&
				strings.Contains(file.content, "目录") &&
				strings.Contains(file.content, "模型") &&
				strings.Contains(file.content, "上下文") {
				return true
			}
		}
		return false
	})
}

func TestCommandsDedupedAndRegistrationSafe(t *testing.T) {
	t.Parallel()
	w, _, _ := startWrapper(t)

	names := map[string]int{}
	for _, ci := range w.Commands() {
		names[ci.Name]++
		for _, a := range ci.Aliases {
			names[a]++
		}
	}
	for name, n := range names {
		require.Equal(t, 1, n, "command %s listed %d times", name, n)
	}
	// Aliases folded into their primary entries.
	var sessions CommandInfo
	found := false
	for _, ci := range w.Commands() {
		if ci.Name == "sessions" {
			sessions = ci
			found = true
		}
	}
	require.True(t, found)
	require.Contains(t, sessions.Aliases, "ls")
	// No entry named after the alias itself.
	for _, ci := range w.Commands() {
		require.NotEqual(t, "ls", ci.Name)
		require.NotEqual(t, "?", ci.Name)
	}

	// Double builtin registration neither panics nor duplicates.
	require.NotPanics(t, func() { registerBuiltinCommands(w) })
	for _, ci := range w.Commands() {
		require.Equal(t, 1, countCommand(w, ci.Name))
	}

	// Builtins cannot be overridden by custom commands.
	require.ErrorIs(t, w.RegisterCommand("help", "hijack", func(context.Context, CommandContext) error { return nil }), ErrCommandExists)
	// A registered custom cannot be re-registered either.
	require.NoError(t, w.RegisterCommand("oneoff", "x", func(context.Context, CommandContext) error { return nil }))
	require.ErrorIs(t, w.RegisterCommand("oneoff", "x", func(context.Context, CommandContext) error { return nil }), ErrCommandExists)
}

func countCommand(w *Wrapper, name string) int {
	n := 0
	for _, ci := range w.Commands() {
		if ci.Name == name {
			n++
		}
	}
	return n
}

func TestSessionsHTMLSearchRobustness(t *testing.T) {
	t.Parallel()

	html := string(RenderSessionsHTML([]SessionSummary{{ID: "s1", Title: "x"}}, ""))
	require.Contains(t, html, `oninput="imwrapFilter()"`)
	require.Contains(t, html, "function imwrapFilter()")
	require.Contains(t, html, "addEventListener('input', imwrapFilter)")
	require.Contains(t, html, "<noscript>")
	require.NotContains(t, html, "=>") // ES5 only
}

// These tests mutate the package-level logger, so they must stay
// sequential (non-parallel).
func TestLoggingHelpersAndFile(t *testing.T) {

	buf := new(strings.Builder)
	prev := Logger()
	SetLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer SetLogger(prev)

	Debug("dbg", "k", "v")
	Info("inf")
	Warn("warn")
	Error("err")
	out := buf.String()
	require.Contains(t, out, "level=DEBUG")
	require.Contains(t, out, "level=INFO")
	require.Contains(t, out, "level=WARN")
	require.Contains(t, out, "level=ERROR")

	path := filepath.Join(t.TempDir(), "test.log")
	prev2 := Logger()
	require.NoError(t, SetLogFileForTest(path, slog.LevelInfo))
	Logger().Info("to-file")
	// SetLogFile keeps stderr too; verify via a direct read of the file.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "to-file")
	SetLogger(prev2)
}

// SetLogFileForTest wraps SetLogFile to keep the parallel test's
// stderr writer from interleaving assertions.
func SetLogFileForTest(path string, level slog.Level) error {
	return SetLogFile(path, level)
}

func TestWorkspaceDirValidated(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	// Nonexistent directory: friendly error notice, no workspace created.
	err := w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d /nonexistent/dir/zzz title"})
	require.ErrorContains(t, err, "目录不可用")
	texts, _ := ad.snapshot()
	require.Len(t, texts, 1)
	require.Contains(t, texts[0], "目录不可用")
	f.mu.Lock()
	require.Len(t, f.workspaces, 1)
	f.mu.Unlock()

	// A file path is rejected too.
	file := filepath.Join(t.TempDir(), "plain.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	err = w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/ask -d " + file + " hi"})
	require.ErrorContains(t, err, "路径不是目录")
	texts, _ = ad.snapshot()
	require.Contains(t, texts[1], "路径不是目录")
}

func TestGitCommandExportsStatusHTML(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()

	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "feature-branch")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "app.go"), []byte("line1\nline2\n"), 0o644))
	run("add", "app.go")
	run("commit", "-m", "init")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "app.go"), []byte("line1\nCHANGED\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "new.txt"), []byte("untracked\n"), 0o644))

	w, f, ad := startWrapper(t)
	ctx := context.Background()
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d " + repo + " git test"}))

	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git"}))
	_, files := ad.snapshot()
	require.Len(t, files, 1)
	require.Contains(t, files[0].filename, "crush-git-")
	require.Contains(t, files[0].content, "feature-branch")
	require.Contains(t, files[0].content, "app.go")
	require.Contains(t, files[0].content, "CHANGED")
	require.Contains(t, files[0].content, "new.txt")
	require.Contains(t, files[0].content, "session-footer")
	texts, _ := ad.snapshot()
	require.Contains(t, texts[1], "feature-branch")
	require.Contains(t, texts[1], "2 处变更")
	_ = f
}

func TestGitCommandNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()

	w, _, ad := startWrapper(t)
	ctx := context.Background()
	plain := t.TempDir()
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/new -d " + plain + " plain"}))
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "c", Text: "/git"}))
	texts, files := ad.snapshot()
	require.Contains(t, texts[1], "不是 git 仓库")
	require.Contains(t, files[0].content, "不是 git 仓库")
}

func TestFileConfigLogFileAndExtra(t *testing.T) {

	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	path := filepath.Join(dir, "imwrap.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"log_level": "debug",
		"log_file": "`+logPath+`",
		"extra": {"myapp": {"theme": "dark", "workers": 4}}
	}`), 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	prev := Logger()
	cfg.Apply(&Config{})
	defer SetLogger(prev)

	require.NotNil(t, cfg.Extra)
	require.Contains(t, string(cfg.Extra["myapp"]), `"theme"`)
	require.Contains(t, string(cfg.Extra["myapp"]), `"dark"`)

	Logger().Info("extra config test")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "extra config test")
}
