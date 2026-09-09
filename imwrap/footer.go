package imwrap

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SessionFooter is the sidebar-like session status block appended to
// HTML reports (turn replies, /git exports): session title and ID,
// workspace directory, effective model, context usage, counters, and
// the workspace's git branch and dirty state.
type SessionFooter struct {
	// SessionID and Title identify the session.
	SessionID string
	Title     string
	// Dir is the workspace directory.
	Dir string
	// Model is "provider/model"; ModelName its display name.
	Model     string
	ModelName string
	// ContextWindow is the model's context window in tokens (0
	// unknown).
	ContextWindow int64
	// PromptTokens / CompletionTokens are the session counters;
	// their sum approximates the context usage.
	PromptTokens     int64
	CompletionTokens int64
	// MessageCount and Cost summarize the session.
	MessageCount int64
	Cost         float64
	// GitBranch is the workspace's current branch, or "" when the
	// directory is not a git repository. GitDirty counts changed
	// files (-1 unknown).
	GitBranch string
	GitDirty  int
	// Queued is the number of prompts queued behind the current run,
	// when known (-1 otherwise).
	Queued int
	// GeneratedAt stamps the footer.
	GeneratedAt time.Time
}

// ContextUsage returns used tokens (prompt+completion) and the usage
// ratio against the context window (0 when unknown).
func (f *SessionFooter) ContextUsage() (used int64, ratio float64) {
	used = f.PromptTokens + f.CompletionTokens
	if f.ContextWindow <= 0 {
		return used, 0
	}
	return used, float64(used) / float64(f.ContextWindow)
}

// collectFooter gathers the footer data for a session. Every piece is
// best-effort: failures leave the zero value rather than failing the
// report.
func (w *Wrapper) collectFooter(ctx context.Context, wsID, sessionID string, queued int) *SessionFooter {
	footer := &SessionFooter{GitDirty: -1, Queued: queued, GeneratedAt: now()}

	if sess, err := w.client.GetSession(ctx, wsID, sessionID); err == nil {
		footer.SessionID = sess.ID
		footer.Title = sess.Title
		footer.PromptTokens = sess.PromptTokens
		footer.CompletionTokens = sess.CompletionTokens
		footer.MessageCount = sess.MessageCount
		footer.Cost = sess.Cost
	}
	footer.Dir = w.pathForWS(wsID)

	if agent, err := w.client.GetAgentInfo(ctx, wsID); err == nil && !agent.IsZero() {
		footer.Model = agent.ModelCfg.Provider + "/" + agent.ModelCfg.Model
		footer.ModelName = agent.Model.Name
		footer.ContextWindow = agent.Model.ContextWindow
	}

	if footer.Dir != "" {
		footer.GitBranch = gitBranch(ctx, footer.Dir)
		footer.GitDirty = gitDirtyCount(ctx, footer.Dir)
	}
	return footer
}

// gitBranch returns the current branch of a repository directory, or
// "" when not a repo / git unavailable.
func gitBranch(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitDirtyCount returns the number of uncommitted changes (-1 when
// unknown).
func gitDirtyCount(ctx context.Context, dir string) int {
	out, err := runGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		return -1
	}
	count := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// runGit runs a git command in dir and returns trimmed stdout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// renderFooterHTML writes the footer block used at the end of HTML
// reports, mirroring the native TUI session sidebar.
func renderFooterHTML(b *strings.Builder, f *SessionFooter) {
	if f == nil {
		return
	}
	used, ratio := f.ContextUsage()
	pct := ratio * 100
	barClass := ""
	switch {
	case ratio >= 0.9:
		barClass = " critical"
	case ratio >= 0.7:
		barClass = " high"
	}

	title := f.Title
	if title == "" {
		title = "(无标题)"
	}
	git := "非 git 仓库"
	if f.GitBranch != "" {
		git = f.GitBranch
		if f.GitDirty > 0 {
			git += fmt.Sprintf("（%d 处未提交）", f.GitDirty)
		} else if f.GitDirty == 0 {
			git += "（干净）"
		}
	}
	window := "未知"
	if f.ContextWindow > 0 {
		window = humanTokens(f.ContextWindow)
	}
	model := f.Model
	if model == "" {
		model = "未设置"
	} else if f.ModelName != "" && f.ModelName != f.Model {
		model += "（" + f.ModelName + "）"
	}

	b.WriteString("<footer class=\"session-footer\">")
	fmt.Fprintf(b, "<div class=\"sf-title\">📌 %s <span class=\"sub\">%s</span></div>", esc(title), esc(shortID(f.SessionID)))
	fmt.Fprintf(b, "<table class=\"sf\"><tr><td>目录</td><td>%s</td></tr>", esc(f.Dir))
	fmt.Fprintf(b, "<tr><td>模型</td><td>%s</td></tr>", esc(model))
	fmt.Fprintf(b, "<tr><td>上下文</td><td>")
	fmt.Fprintf(b, "%s / %s tokens（%.1f%%）", humanTokens(used), window, pct)
	fmt.Fprintf(b, "<span class=\"sf-bar%s\"><span style=\"width:%.1f%%\"></span></span>", barClass, pct)
	b.WriteString("</td></tr>")
	fmt.Fprintf(b, "<tr><td>统计</td><td>%d 条消息，%.4f $，prompt %s / completion %s</td></tr>",
		f.MessageCount, f.Cost, humanTokens(f.PromptTokens), humanTokens(f.CompletionTokens))
	fmt.Fprintf(b, "<tr><td>Git</td><td>%s</td></tr>", esc(git))
	if f.Queued >= 0 {
		fmt.Fprintf(b, "<tr><td>队列</td><td>%d 条待处理</td></tr>", f.Queued)
	}
	fmt.Fprintf(b, "<tr><td>生成时间</td><td>%s</td></tr></table></footer>",
		esc(f.GeneratedAt.Format("2006-01-02 15:04:05")))
}
