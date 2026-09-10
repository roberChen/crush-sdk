package imwrap

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
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
		for _, f := range parseUnifiedDiffFiles(g.Diff) {
			renderUnifiedFileHTML(&b, f)
		}
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

// GitCommit is one entry of the recent-commit log with its diff.
type GitCommit struct {
	// Hash is the abbreviated commit hash.
	Hash string
	// Subject is the one-line commit message.
	Subject string
	// Author and Date describe the commit metadata.
	Author string
	Date   string
	// Diff is the commit's full diff (`git show`), empty for merge
	// commits without a text diff.
	Diff string
}

// GitLog collects the n most recent commits of dir with their diffs
// (each via `git show`); combined additionally returns the squashed
// diff across the n commits (`HEAD~n..HEAD`).
func GitLog(ctx context.Context, dir string, n int) ([]GitCommit, string, error) {
	if n <= 0 {
		n = 3
	}
	out, err := runGit(ctx, dir, "log", "-n", fmt.Sprint(n), "--pretty=format:%h%x00%s%x00%an%x00%ad", "--date=iso")
	if err != nil {
		return nil, "", fmt.Errorf("git log 失败: %w", err)
	}
	var commits []GitCommit
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\x00")
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, GitCommit{Hash: parts[0], Subject: parts[1], Author: parts[2], Date: parts[3]})
	}
	for i := range commits {
		diff, err := runGit(ctx, dir, "show", "--format=", "--unified=3", commits[i].Hash)
		if err == nil {
			commits[i].Diff = diff
		}
	}
	combined := combinedRangeDiff(ctx, dir, commits)
	return commits, combined, nil
}

// combinedRangeDiff builds the squashed diff across the listed
// commits. HEAD~n does not exist when the repo has exactly n
// commits, so anchor on the oldest listed commit's parent and fall
// back to the repository root.
func combinedRangeDiff(ctx context.Context, dir string, commits []GitCommit) string {
	if len(commits) == 0 {
		return ""
	}
	oldest := commits[len(commits)-1].Hash
	if parent, err := runGit(ctx, dir, "rev-parse", "--verify", oldest+"^"); err == nil {
		if diff, err := runGit(ctx, dir, "diff", strings.TrimSpace(parent)+"..HEAD", "--unified=3"); err == nil {
			return diff
		}
	}
	// Oldest is the root commit: show it plus everything after it.
	rootShow, errShow := runGit(ctx, dir, "show", "--format=", "--unified=3", oldest)
	rest, errRest := runGit(ctx, dir, "diff", oldest+"..HEAD", "--unified=3")
	if errShow != nil {
		return ""
	}
	if errRest != nil {
		return rootShow
	}
	return rootShow + rest
}

// RenderGitLogHTML renders recent commits with their diffs (each
// commit a separate section) plus the combined diff section when
// present, ending with the session footer.
func RenderGitLogHTML(commits []GitCommit, combined string, footer *SessionFooter) []byte {
	var b strings.Builder
	b.WriteString(htmlPageStart())
	b.WriteString("<header class=\"doc-header\"><h1>Git 最近提交</h1>")
	fmt.Fprintf(&b, "<p class=\"sub\">%d 个提交，generated at %s</p></header>",
		len(commits), esc(now().Format("2006-01-02 15:04:05")))

	b.WriteString("<main>")
	for i, c := range commits {
		fmt.Fprintf(&b, "<div class=\"commit-head\">%s %s <span class=\"sub\">%s · %s</span></div>",
			esc(c.Hash), esc(c.Subject), esc(c.Author), esc(c.Date))
		if strings.TrimSpace(c.Diff) == "" {
			fmt.Fprintf(&b, "<p class=\"sub\">（提交 %d 无文本 diff）</p>", i+1)
			continue
		}
		for _, f := range parseUnifiedDiffFiles(c.Diff) {
			renderUnifiedFileHTML(&b, f)
		}
	}
	if strings.TrimSpace(combined) != "" {
		b.WriteString("<div class=\"commit-head\">合并视图（跨全部列出的提交）</div>")
		for _, f := range parseUnifiedDiffFiles(combined) {
			renderUnifiedFileHTML(&b, f)
		}
	}
	b.WriteString("</main>")

	renderFooterHTML(&b, footer)
	b.WriteString(htmlPageEnd())
	return []byte(b.String())
}

// gitRunSimple runs a mutating git subcommand (push/pull/checkout)
// and returns its combined output for a text reply.
func gitRunSimple(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return "", errors.New(text)
		}
		return "", err
	}
	if text == "" {
		text = "(无输出)"
	}
	return text, nil
}

func cmdGit(ctx context.Context, c CommandContext) error {
	sub := ""
	args := c.Args
	known := false
	if len(args) > 0 {
		switch args[0] {
		case "log", "push", "pull", "checkout", "co", "branch":
			sub = args[0]
			args = args[1:]
			known = true
		}
	}
	switch {
	case len(c.Args) == 0:
		return c.W.ExportGitStatus(ctx, c.ChatID)
	case !known:
		return c.Reply(ctx, "用法: /git [log [数量]] | push [remote [分支]] | pull | checkout <分支> | branch | （无参数=导出状态）")
	case sub == "log":
		return cmdGitLog(ctx, c, args)
	default:
		return cmdGitSimple(ctx, c, sub, args)
	}
}

func cmdGitLog(ctx context.Context, c CommandContext, args []string) error {
	flags, rest := parseFlagArgs(args)
	n := 3
	if v := flagValue(flags, "n", "num", "count"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	} else if len(rest) > 0 {
		if parsed, err := strconv.Atoi(rest[0]); err == nil {
			n = parsed
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 50 {
		n = 50
	}

	w := c.W
	sessionID, wsID, err := w.ensureSession(ctx, c.ChatID)
	if err != nil {
		return err
	}
	dir := w.pathForWS(wsID)
	commits, combined, err := GitLog(ctx, dir, n)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return c.Reply(ctx, "仓库没有任何提交。")
	}
	footer := w.collectFooter(ctx, wsID, sessionID, -1)
	name := fmt.Sprintf("crush-gitlog-%s.html", timestampSlug(now()))
	if err := c.ReplyFile(ctx, name, RenderGitLogHTML(commits, combined, footer)); err != nil {
		return err
	}
	return c.Reply(ctx, fmt.Sprintf("📜 最近 %d 个提交（含各自 diff 与合并视图），详见 HTML 文件。", len(commits)))
}

func cmdGitSimple(ctx context.Context, c CommandContext, sub string, args []string) error {
	if sub == "co" {
		sub = "checkout"
	}
	w := c.W
	_, wsID, err := w.ensureSession(ctx, c.ChatID)
	if err != nil {
		return err
	}
	dir := w.pathForWS(wsID)

	gitArgs := []string{sub}
	gitArgs = append(gitArgs, args...)
	switch sub {
	case "checkout":
		if len(args) == 0 {
			out, err := gitRunSimple(ctx, dir, "branch", "--format=%(refname:short)")
			if err != nil {
				return err
			}
			return c.Reply(ctx, "本地分支：\n"+out)
		}
	case "push":
		if len(args) == 0 {
			gitArgs = []string{"push"}
		}
	case "pull":
		gitArgs = []string{"pull"}
	}

	out, err := gitRunSimple(ctx, dir, gitArgs...)
	if err != nil {
		return c.Reply(ctx, "⚠ git "+sub+" 失败: "+err.Error())
	}
	return c.Reply(ctx, "✅ git "+sub+"\n"+out)
}
