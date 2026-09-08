package imwrap

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crush "github.com/roberChen/crush-sdk"
	"github.com/stretchr/testify/require"
)

func TestMarkdownHeadingsListsQuotesHR(t *testing.T) {
	t.Parallel()

	md := "# Title\n\n## Sub **bold**\n\n- one\n- two\n\n1. first\n2. second\n\n> quoted **text**\n\n---\n\nplain paragraph\nsoft break"
	out := markdownish(md)

	require.Contains(t, out, "<h1>Title</h1>")
	require.Contains(t, out, "<h2>Sub <strong>bold</strong></h2>")
	require.Contains(t, out, "<ul><li>one</li><li>two</li></ul>")
	require.Contains(t, out, "<ol><li>first</li><li>second</li></ol>")
	require.Contains(t, out, "<blockquote><p>quoted <strong>text</strong></p></blockquote>")
	require.Contains(t, out, "<hr>")
	require.Contains(t, out, "<p>plain paragraph<br>soft break</p>")
}

func TestMarkdownTable(t *testing.T) {
	t.Parallel()

	md := "| Name | Count | Note |\n| --- | :---: | ---: |\n| `bash` | 3 | **fast** |\n| view | 12 | ok |"
	out := markdownish(md)

	require.Contains(t, out, `<table class="md"><thead><tr><th>Name</th><th style="text-align:center">Count</th><th style="text-align:right">Note</th></tr>`)
	require.Contains(t, out, "<td><code>bash</code></td>")
	require.Contains(t, out, `<td style="text-align:center">3</td>`)
	require.Contains(t, out, `<td style="text-align:right"><strong>fast</strong></td>`)
	require.Contains(t, out, `<td style="text-align:center">12</td>`)
	require.Contains(t, out, `<td style="text-align:right">ok</td>`)
	require.Contains(t, out, "</tbody></table>")
}

func TestMarkdownInlineExtras(t *testing.T) {
	t.Parallel()

	out := markdownish("~~gone~~ and ![logo](https://x/l.png) and visit https://example.com/a?b=1 now")
	require.Contains(t, out, "<del>gone</del>")
	require.Contains(t, out, `<a href="https://x/l.png" target="_blank" rel="noreferrer">🖼 logo</a>`)
	require.Contains(t, out, `<a href="https://example.com/a?b=1" target="_blank" rel="noreferrer">https://example.com/a?b=1</a>`)

	// Autolink must not touch URLs already inside generated markup.
	out = markdownish("[docs](https://example.com/d)")
	require.Equal(t, `<p><a href="https://example.com/d" target="_blank" rel="noreferrer">docs</a></p>`, out)
}

func TestMarkdownEscapingHeld(t *testing.T) {
	t.Parallel()

	out := markdownish("# <b>x</b>\n\n| a |\n| --- |\n| <img src=x> |")
	require.NotContains(t, out, "<b>x</b>")
	require.Contains(t, out, "&lt;b&gt;x&lt;/b&gt;")
	require.NotContains(t, out, "<img src=x>")
}

func TestMarkdownFencesStillWork(t *testing.T) {
	t.Parallel()

	out := markdownish("before ```x=<b>``` after")
	require.Contains(t, out, "x=&lt;b&gt;")
	require.NotContains(t, strings.ReplaceAll(out, "x=&lt;b&gt;", ""), "<b>")
}

func TestEchoFilterDropsWrapperOutput(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)

	sendPrompt(t, w, f, "hello agent")
	// The wrapper's own acks and summaries must not trigger new turns.
	for _, text := range ad.snapshotTexts(t, "") {
		require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "chat1", Text: text}))
	}
	f.mu.Lock()
	sent := len(f.agentMsgs)
	f.mu.Unlock()
	require.Equal(t, 1, sent) // only the original prompt
}

func TestEchoFilterByMessageIDAndSelfAccount(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	ad := &fakeAdapter{}
	w, err := New(Config{Client: f.client(t), Adapter: ad, SelfAccount: "bot@im"})
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(w.Stop)

	// Sender matching the bot account is dropped.
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", Sender: "bot@im", Text: "deploy now"}))
	// Marked message IDs are dropped.
	w.MarkSelfMessage("c", "im-msg-42")
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", ID: "im-msg-42", Text: "anything"}))
	// Explicit FromSelf is dropped.
	require.NoError(t, w.HandleMessage(context.Background(), IMMessage{ChatID: "c", FromSelf: true, Text: "echo"}))

	require.Empty(t, f.agentMessages())
	require.Empty(t, ad.snapshotTexts(t, ""))

	// IsSelfOutput reflects the filters.
	require.True(t, w.IsSelfOutput(IMMessage{ChatID: "c", ID: "im-msg-42", Text: "x"}))
	require.True(t, w.IsSelfOutput(IMMessage{ChatID: "c", Sender: "bot@im", Text: "x"}))
	require.False(t, w.IsSelfOutput(IMMessage{ChatID: "c", Sender: "alice", Text: "unique user text"}))
}

func TestEchoFilterSentAtGuard(t *testing.T) {
	t.Parallel()

	tr := newEchoTracker(time.Minute)
	now := time.Now()
	tr.nowFunc = func() time.Time { return now }

	tr.record("c", "yes")
	// Same text arriving after the recording: the bot's own echo.
	require.True(t, tr.match("c", "", "yes", now.Add(time.Second)))
	// Same text sent before the recording: a user message.
	require.False(t, tr.match("c", "", "yes", now.Add(-time.Minute)))
	// Different text passes.
	require.False(t, tr.match("c", "", "no", now))
	// Other chats are unaffected.
	require.False(t, tr.match("other", "", "yes", now))
}

func TestEchoTrackerExpiry(t *testing.T) {
	t.Parallel()

	tr := newEchoTracker(50 * time.Millisecond)
	now := time.Now()
	tr.nowFunc = func() time.Time { return now }
	tr.record("c", "ping")
	now = now.Add(time.Minute)
	require.False(t, tr.match("c", "", "ping", now))
}

func TestFileConfigLoadApply(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "imwrap.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"command_prefix": "!",
		"session_title": "from-file",
		"self_account": "bot@im",
		"echo_window_seconds": 30,
		"start_server": true,
		"server_command": "/opt/crush/bin/crush",
		"server_start_timeout_seconds": 5,
		"log_level": "debug"
	}`), 0o600))

	missing := filepath.Join(dir, "missing.json")
	cfg, err := LoadConfig(missing, path)
	require.NoError(t, err)
	require.Equal(t, "!", cfg.CommandPrefix)

	_, err = LoadConfig(missing)
	require.Error(t, err)

	var applied Config
	cfg.Apply(&applied)
	require.Equal(t, "!", applied.CommandPrefix)
	require.Equal(t, "from-file", applied.SessionTitle)
	require.Equal(t, "bot@im", applied.SelfAccount)
	require.Equal(t, 30*time.Second, applied.EchoWindow)
	require.True(t, applied.StartServer)
	require.Equal(t, "/opt/crush/bin/crush", applied.ServerCommand)
	require.Equal(t, 5*time.Second, applied.ServerStartTimeout)

	require.Equal(t, slog.LevelDebug, parseLogLevel("DEBUG"))
	require.Equal(t, slog.LevelInfo, parseLogLevel("bogus"))
}

func TestSummarizeCommand(t *testing.T) {
	t.Parallel()
	w, f, ad := startWrapper(t)
	ctx := context.Background()

	sent := sendPrompt(t, w, f, "fill the context")
	require.NoError(t, w.HandleMessage(ctx, IMMessage{ChatID: "chat1", Text: "/summarize"}))

	waitFor(t, "summarize request", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.summarized) == 1 && f.summarized[0] == "ws1/"+sent.SessionID
	})
	require.Len(t, ad.findTexts(t, "已请求压缩会话"), 1)
}

func TestAutostartDisabledByDefaultAndFailsFastWithBogusCommand(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	ad := &fakeAdapter{}

	// Default: no autostart; a reachable server is used as-is.
	w, err := New(Config{Client: f.client(t), Adapter: ad})
	require.NoError(t, err)
	require.False(t, w.cfg.StartServer)
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(w.Stop)
	require.Equal(t, "ws1", w.WorkspaceID())

	// With autostart against an unreachable server and a bogus
	// command, Start fails fast with a clear error.
	dead := newDeadClient(t)
	w2, err := New(Config{Client: dead, Adapter: ad, StartServer: true, ServerCommand: "/nonexistent/crush", ServerStartTimeout: 1500 * time.Millisecond})
	require.NoError(t, err)
	err = w2.Start(context.Background())
	require.ErrorContains(t, err, "failed to start /nonexistent/crush")
	w2.Stop()
}

// newDeadClient returns a client pointed at a closed port so Health
// fails immediately.
func newDeadClient(t *testing.T) *crush.Client {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	deadURL := dead.URL
	dead.Close()
	host := strings.TrimPrefix(deadURL, "http://")
	c, err := crush.NewClient(t.TempDir(), "tcp", host)
	require.NoError(t, err)
	return c
}

func TestMarkdownTableNotConfusedByPipesInCode(t *testing.T) {
	t.Parallel()

	out := markdownish("cmd `a|b` alone")
	require.Contains(t, out, "<code>a|b</code>")
	require.NotContains(t, out, "<table")
}

func (f *fakeAdapter) snapshotTexts(t *testing.T, substr string) []string {
	t.Helper()
	texts, _ := f.snapshot()
	if substr == "" {
		return texts
	}
	var out []string
	for _, s := range texts {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}
