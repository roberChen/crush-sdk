package imwrap

import (
	"context"
	"fmt"
	"strings"
)

// GitFileStatus is one entry of `git status --porcelain`.
type GitFileStatus struct {
	// XY are the index/worktree status codes (e.g. "M ", " M", "??").
	XY string
	// Path is the file path (renames show "old -> new").
	Path string
}

// GitStatus is a snapshot of the workspace repository: branch,
// changed files, and the combined diff against HEAD.
type GitStatus struct {
	// Branch is the current branch ("" when not a repository).
	Branch string
	// Files lists changed files from git status --porcelain.
	Files []GitFileStatus
	// Diff is the combined unstaged+staged diff against HEAD
	// (tracked files only; untracked file contents are excluded).
	Diff string
	// NotRepo is true when the directory is not a git repository.
	NotRepo bool
}

// collectGitStatus shells out to git in dir. When git is missing or
// the directory is not a repository, NotRepo is set and the command
// reports that instead of failing.
func collectGitStatus(ctx context.Context, dir string) (*GitStatus, error) {
	st := &GitStatus{}
	if out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
		st.NotRepo = true
		return st, nil
	} else {
		st.Branch = strings.TrimSpace(out)
	}

	if out, err := runGit(ctx, dir, "status", "--porcelain"); err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimRight(line, "\r")
			if len(strings.TrimSpace(line)) < 4 {
				continue
			}
			st.Files = append(st.Files, GitFileStatus{
				XY:   line[:2],
				Path: strings.TrimSpace(line[3:]),
			})
		}
	}

	if out, err := runGit(ctx, dir, "diff", "HEAD", "--unified=3"); err == nil {
		st.Diff = out
	}
	return st, nil
}

// RenderGitStatusHTML renders the workspace git status (branch,
// changed files, full diff) plus the session footer as a
// self-contained HTML document.
func RenderGitStatusHTML(g *GitStatus, footer *SessionFooter) []byte {
	var b strings.Builder
	b.WriteString(htmlPageStart())
	b.WriteString("<header class=\"doc-header\"><h1>Git 状态</h1>")
	if g.NotRepo {
		b.WriteString("<p class=\"warn\">当前目录不是 git 仓库。</p></header>")
		b.WriteString(htmlPageEnd())
		return []byte(b.String())
	}
	fmt.Fprintf(&b, "<p class=\"sub\">分支 %s，%d 处变更，generated at %s</p></header>",
		esc(g.Branch), len(g.Files), esc(now().Format("2006-01-02 15:04:05")))

	b.WriteString("<main>")
	if len(g.Files) == 0 {
		b.WriteString("<p class=\"sub\">工作区干净，没有未提交的变更。</p>")
	} else {
		b.WriteString("<table class=\"md\"><thead><tr><th>状态</th><th>文件</th></tr></thead><tbody>")
		for _, f := range g.Files {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>", esc(f.XY), esc(f.Path))
		}
		b.WriteString("</tbody></table>")
	}
	if strings.TrimSpace(g.Diff) != "" {
		b.WriteString("<div class=\"hunk-head\">完整 diff（相对 HEAD，含已暂存）</div>")
		renderDiffLinesHTML(&b, parseUnifiedDiff(g.Diff))
	}
	b.WriteString("</main>")

	renderFooterHTML(&b, footer)
	b.WriteString(htmlPageEnd())
	return []byte(b.String())
}

// ExportGitStatus renders the chat's current workspace git status
// (branch, changed files, full diff) plus the session footer as an
// HTML file and sends it. It backs the /git builtin and is also
// usable programmatically.
func (w *Wrapper) ExportGitStatus(ctx context.Context, chatID string) error {
	sessionID, wsID, err := w.ensureSession(ctx, chatID)
	if err != nil {
		return err
	}
	dir := w.pathForWS(wsID)
	st, err := collectGitStatus(ctx, dir)
	if err != nil {
		return fmt.Errorf("failed to collect git status: %w", err)
	}
	footer := w.collectFooter(ctx, wsID, sessionID, -1)

	name := fmt.Sprintf("crush-git-%s.html", timestampSlug(now()))
	if err := w.sendFile(ctx, chatID, name, RenderGitStatusHTML(st, footer)); err != nil {
		return err
	}
	if st.NotRepo {
		return w.sendText(ctx, chatID, "⚠ 目录 "+dir+" 不是 git 仓库。")
	}
	return w.sendText(ctx, chatID, fmt.Sprintf("🌿 分支 %s，%d 处变更，详见 HTML 文件。", st.Branch, len(st.Files)))
}
