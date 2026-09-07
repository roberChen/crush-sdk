package imwrap

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/tools"
)

// toolRegistry lists the tool names the Crush server exposes; it
// mirrors the tools package constants (which must stay in sync with
// the server's registry).
var toolRegistry = []string{
	tools.AgenticFetchToolName,
	tools.BashToolName,
	tools.DownloadToolName,
	tools.EditToolName,
	tools.FetchToolName,
	tools.LSToolName,
	tools.MultiEditToolName,
	tools.ViewToolName,
	tools.WriteToolName,
}

// SessionInfo is a snapshot of everything interesting about a chat's
// current session, backing the /info command.
type SessionInfo struct {
	// Session is the session record (title, token usage, cost...).
	Session SessionSnapshot
	// Dir is the workspace directory the session runs in.
	Dir string
	// WorkspaceID is the owning workspace.
	WorkspaceID string
	// Model is the effective model as "provider/model".
	Model string
	// ModelName is the model's display name.
	ModelName string
	// ContextWindow is the model's context window in tokens (0 when
	// unknown).
	ContextWindow int64
	// PromptTokens / CompletionTokens mirror the session counters;
	// PromptTokens approximates the current context watermark.
	PromptTokens     int64
	CompletionTokens int64
	// Skills lists discovered skills in the workspace.
	Skills []SkillInfo
	// Tools lists enabled tool names; DisabledTools the ones turned
	// off by config.
	Tools         []string
	DisabledTools []string
}

// SessionSnapshot carries the proto.Session fields worth reporting.
type SessionSnapshot struct {
	ID               string
	Title            string
	MessageCount     int64
	Cost             float64
	PromptTokens     int64
	CompletionTokens int64
}

// SkillInfo describes one discovered skill.
type SkillInfo struct {
	Name  string
	Path  string
	State string
	Error string
}

// ContextWatermark returns the context usage ratio (0..1) and a
// human-readable "used/total" string. It returns 0 and "unknown"
// when the context window is not known.
func (i *SessionInfo) ContextWatermark() (float64, string) {
	if i.ContextWindow <= 0 {
		return 0, "unknown"
	}
	used := max(i.PromptTokens, 0)
	ratio := float64(used) / float64(i.ContextWindow)
	return ratio, fmt.Sprintf("%d/%d tokens (%.1f%%)", used, i.ContextWindow, ratio*100)
}

// GetSessionInfo collects the chat's current session info: session
// counters, workspace directory, effective model and context window,
// context watermark, discovered skills, and enabled tools.
func (w *Wrapper) GetSessionInfo(ctx context.Context, chatID string) (*SessionInfo, error) {
	sessionID, wsID, err := w.ensureSession(ctx, chatID)
	if err != nil {
		return nil, err
	}
	info := &SessionInfo{WorkspaceID: wsID}

	if sess, err := w.client.GetSession(ctx, wsID, sessionID); err == nil {
		info.Session = SessionSnapshot{
			ID:               sess.ID,
			Title:            sess.Title,
			MessageCount:     sess.MessageCount,
			Cost:             sess.Cost,
			PromptTokens:     sess.PromptTokens,
			CompletionTokens: sess.CompletionTokens,
		}
		info.PromptTokens = sess.PromptTokens
		info.CompletionTokens = sess.CompletionTokens
	}

	if ws, err := w.client.GetWorkspace(ctx, wsID); err == nil {
		info.Dir = ws.Path
		for _, s := range ws.Skills {
			state := "ok"
			if s.State == proto.SkillStateError {
				state = "error"
			}
			info.Skills = append(info.Skills, SkillInfo{Name: s.Name, Path: s.Path, State: state, Error: s.Error})
		}
		sort.Slice(info.Skills, func(i, j int) bool { return info.Skills[i].Name < info.Skills[j].Name })
	}
	if info.Dir == "" {
		info.Dir = w.pathForWS(wsID)
	}

	if agent, err := w.client.GetAgentInfo(ctx, wsID); err == nil && !agent.IsZero() {
		info.Model = agent.ModelCfg.Provider + "/" + agent.ModelCfg.Model
		info.ModelName = agent.Model.Name
		info.ContextWindow = agent.Model.ContextWindow
	}

	disabled := map[string]bool{}
	if cfg, err := w.client.GetConfig(ctx, wsID); err == nil && cfg.Options != nil {
		for _, t := range cfg.Options.DisabledTools {
			disabled[t] = true
		}
	}
	for _, t := range toolRegistry {
		if disabled[t] {
			info.DisabledTools = append(info.DisabledTools, t)
			continue
		}
		info.Tools = append(info.Tools, t)
	}
	return info, nil
}

// FormatSessionInfo renders SessionInfo as an IM-friendly text block.
func FormatSessionInfo(info *SessionInfo) string {
	var b strings.Builder
	title := info.Session.Title
	if title == "" {
		title = "(无标题)"
	}
	fmt.Fprintf(&b, "📋 会话 %s（%s）\n", shortID(info.Session.ID), title)
	fmt.Fprintf(&b, "目录: %s\n", info.Dir)
	if info.Model != "" {
		name := info.ModelName
		if name == "" || name == info.Model {
			fmt.Fprintf(&b, "模型: %s\n", info.Model)
		} else {
			fmt.Fprintf(&b, "模型: %s（%s）\n", info.Model, name)
		}
	}
	ratio, usage := info.ContextWatermark()
	switch {
	case ratio >= 0.9:
		fmt.Fprintf(&b, "上下文水位: %s ⚠ 接近上限\n", usage)
	case ratio >= 0.7:
		fmt.Fprintf(&b, "上下文水位: %s ⚠ 偏高\n", usage)
	default:
		fmt.Fprintf(&b, "上下文水位: %s\n", usage)
	}
	fmt.Fprintf(&b, "技能: %d 个\n", len(info.Skills))
	for _, s := range slices.Clip(info.Skills) {
		if s.Error != "" || s.State == "error" {
			fmt.Fprintf(&b, "  ✗ %s (%s)\n", s.Name, s.Error)
			continue
		}
		fmt.Fprintf(&b, "  ✓ %s\n", s.Name)
	}
	fmt.Fprintf(&b, "工具: %s\n", strings.Join(info.Tools, ", "))
	if len(info.DisabledTools) > 0 {
		fmt.Fprintf(&b, "已禁用工具: %s\n", strings.Join(info.DisabledTools, ", "))
	}
	fmt.Fprintf(&b, "统计: %d 条消息，%.4f $，prompt %d tok，completion %d tok\n",
		info.Session.MessageCount, info.Session.Cost, info.PromptTokens, info.CompletionTokens)
	return b.String()
}
