package imwrap

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SessionSummary is one row of the cross-workspace session listing
// rendered by /sessions (HTML) and available to host programs via
// [Wrapper.SessionSummaries].
type SessionSummary struct {
	// ID is the full session ID (switch with /switch <ID prefix or
	// list index>).
	ID string
	// Title is the session title.
	Title string
	// MessageCount, PromptTokens, CompletionTokens and Cost mirror
	// the session counters.
	MessageCount     int64
	PromptTokens     int64
	CompletionTokens int64
	Cost             float64
	// UpdatedAt is unix seconds.
	UpdatedAt int64
	// Busy marks a session with an in-flight run.
	Busy bool
	// WorkspacePath is the directory the session's workspace runs in.
	WorkspacePath string
	// WorkspaceID is the owning workspace.
	WorkspaceID string
	// Current marks the chat's bound session.
	Current bool
}

// sessionSummaries converts the aggregated session entries into
// summaries, marking the chat's current binding. Caller supplies the
// entries from listAllSessions.
func sessionSummaries(entries []sessionEntry, currentWS, currentSession string) []SessionSummary {
	out := make([]SessionSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, SessionSummary{
			ID:               e.sess.ID,
			Title:            e.sess.Title,
			MessageCount:     e.sess.MessageCount,
			PromptTokens:     e.sess.PromptTokens,
			CompletionTokens: e.sess.CompletionTokens,
			Cost:             e.sess.Cost,
			UpdatedAt:        e.sess.UpdatedAt,
			Busy:             e.sess.IsBusy,
			WorkspacePath:    e.path,
			WorkspaceID:      e.wsID,
			Current:          e.sess.ID == currentSession && e.wsID == currentWS,
		})
	}
	return out
}

// filterSessionSummaries keeps summaries whose ID, title, or
// workspace path contains the (case-insensitive) keyword.
func filterSessionSummaries(summaries []SessionSummary, keyword string) []SessionSummary {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return summaries
	}
	out := make([]SessionSummary, 0, len(summaries))
	for _, s := range summaries {
		hay := strings.ToLower(s.ID + " " + s.Title + " " + s.WorkspacePath)
		if strings.Contains(hay, kw) {
			out = append(out, s)
		}
	}
	return out
}

// SessionSummaries lists sessions across every known workspace, most
// recently updated first, optionally filtered server-side by keyword
// (matched against session ID, title, and workspace path). The chat's
// bound session is marked Current.
func (w *Wrapper) SessionSummaries(ctx context.Context, chatID, keyword string) ([]SessionSummary, error) {
	entries, err := w.listAllSessions(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	st := w.state(chatID)
	currentSession := st.sessionID
	w.mu.Unlock()
	summaries := sessionSummaries(entries, w.chatWorkspace(chatID), currentSession)
	return filterSessionSummaries(summaries, keyword), nil
}

// RenderSessionsHTML renders a session listing to a self-contained
// HTML document with a client-side search box: typing filters rows by
// session ID, title, workspace directory, or status. filter is the
// server-side keyword already applied (shown in the header), or "".
func RenderSessionsHTML(summaries []SessionSummary, filter string) []byte {
	var b strings.Builder
	b.WriteString(htmlPageStart())
	b.WriteString("<header class=\"doc-header\"><h1>会话列表</h1>")
	fmt.Fprintf(&b, "<p class=\"sub\">共 %d 个会话", len(summaries))
	if filter != "" {
		fmt.Fprintf(&b, "（关键词 %s）", esc(filter))
	}
	fmt.Fprintf(&b, "，generated at %s</p>", esc(time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("<input id=\"q\" class=\"search\" type=\"search\" placeholder=\"搜索：标题 / 会话ID / 目录 / 状态（当前、busy）\" autocomplete=\"off\" oninput=\"imwrapFilter()\">")
	b.WriteString("<span id=\"search-note\" class=\"sub\"></span>")
	b.WriteString("<noscript><p class=\"warn\">当前查看器禁用了脚本，无法实时搜索；可在 IM 中使用 /sessions &lt;关键词&gt; 服务端过滤后重新导出。</p></noscript>")
	b.WriteString("</header>")

	b.WriteString("<main><table class=\"sessions\"><thead><tr><th>#</th><th>ID</th><th>标题</th><th>消息</th><th>tokens</th><th>费用</th><th>更新时间</th><th>目录</th><th>状态</th></tr></thead><tbody>")
	for i, s := range summaries {
		title := s.Title
		if title == "" {
			title = "(无标题)"
		}
		updated := "-"
		if s.UpdatedAt > 0 {
			updated = time.Unix(s.UpdatedAt, 0).Format("01-02 15:04")
		}
		status := ""
		if s.Current {
			status += "▶当前"
		}
		if s.Busy {
			if status != "" {
				status += " "
			}
			status += "busy"
		}
		if status == "" {
			status = "-"
		}
		fmt.Fprintf(&b, "<tr%s data-search=\"%s\"><td>%d</td><td title=\"%s\">%s</td><td>%s</td><td>%d</td><td>%s</td><td>%.4f</td><td>%s</td><td>%s</td><td>%s</td></tr>",
			currentClass(s.Current), esc(sessionSearchBlob(s)), i+1, esc(s.ID), esc(shortID(s.ID)), esc(title),
			s.MessageCount, esc(tokenPair(s)), s.Cost, esc(updated), esc(s.WorkspacePath), esc(status))
	}
	b.WriteString("</tbody></table></main>")

	b.WriteString(sessionsSearchJS)
	b.WriteString(htmlPageEnd())
	return []byte(b.String())
}

func sessionSearchBlob(s SessionSummary) string {
	status := ""
	if s.Current {
		status += " 当前 current"
	}
	if s.Busy {
		status += " busy"
	}
	return strings.ToLower(s.ID + " " + s.Title + " " + s.WorkspacePath + status)
}

func currentClass(current bool) string {
	if current {
		return " class=\"current\""
	}
	return ""
}

func tokenPair(s SessionSummary) string {
	if s.PromptTokens == 0 && s.CompletionTokens == 0 {
		return "-"
	}
	return fmt.Sprintf("%dk/%dk", s.PromptTokens/1000, s.CompletionTokens/1000)
}

// sessionsSearchJS wires the search box to row filtering; it is
// inline so the document stays self-contained.
// sessionsSearchJS wires the search box to row filtering. It uses
// only classic ES5 constructs (no NodeList.forEach, no arrow
// functions) and a named global so the inline oninput fallback works
// even where addEventListener is blocked; it stays inline so the
// document is self-contained.
const sessionsSearchJS = `<script>
function imwrapFilter() {
  var q = document.getElementById('q');
  if (!q) return;
  var v = q.value.trim().toLowerCase();
  var rows = document.querySelectorAll('table.sessions tbody tr');
  var shown = 0;
  for (var i = 0; i < rows.length; i++) {
    var tr = rows[i];
    var hay = tr.getAttribute('data-search') || '';
    var hit = !v || hay.indexOf(v) !== -1;
    tr.style.display = hit ? '' : 'none';
    if (hit) shown++;
  }
  var note = document.getElementById('search-note');
  if (note) note.textContent = v ? (shown + ' / ' + rows.length + ' 匹配') : '';
}
(function () {
  var q = document.getElementById('q');
  if (!q) return;
  q.addEventListener('input', imwrapFilter);
  q.addEventListener('search', imwrapFilter);
})();</script>`
