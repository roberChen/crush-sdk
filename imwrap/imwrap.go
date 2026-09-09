// Package imwrap is a chat framework over the crush-sdk client for
// driving Crush from IM (instant messaging) software such as WeCom,
// Lark, Slack, Telegram, or WhatsApp bots. The host program only
// provides the IM integration ([IMAdapter], optionally
// [HistoryFetcher]) plus custom commands; the framework owns
// everything else: sessions, permissions, reporting, questions,
// models, filtering, logging, and configuration.
//
// # Messaging scenarios
//
// Two conversation layouts are supported, and the framework
// distinguishes user input from the bot's own output before anything
// else runs (see [Wrapper.HandleMessage] and [Wrapper.IsSelfOutput]):
//
//   - Two accounts: the user and the bot are separate IM accounts.
//     Set [Config.SelfAccount] to the bot's account; messages from it
//     are dropped.
//   - Shared account: the bot polls the conversation history under
//     the same account, so its own output flows back into the feed.
//     The framework records every text it sends and recognizes the
//     echo (chat-scoped, time-windowed content match with a
//     message-timestamp direction guard). When the host knows the IM
//     message IDs of its own sends, [Wrapper.MarkSelfMessage] enables
//     exact ID matching; [IMMessage.FromSelf] declares direction
//     outright.
//
// # What the framework does
//
//   - Parses messages: the command prefix ("/" by default) marks
//     commands; everything else becomes an agent prompt, acked
//     immediately.
//   - Runs prompts on a per-conversation Crush session with
//     permissions auto-granted (YOLO): workspaces are created with
//     the auto-approve flag and SSE permission requests are granted
//     automatically (both opt-out via [Config.DisableYOLO] and
//     [Config.DisableAutoGrant]).
//   - Reports each completed turn as a self-contained HTML file:
//     thinking, assistant text (full markdown: headings, tables with
//     alignment, lists, quotes, rules, inline markup), tool calls
//     paired with their results, colored diffs for edit/write/
//     multiedit, and nested sub-agent transcripts plus labeled output
//     for task/agent calls. Sub-agent sessions never steal the chat's
//     binding.
//   - Handles the question tool: sends the partial turn as HTML plus
//     a text question; the next non-command reply is parsed (choice
//     index or label, yes/no, free text, numbered lines for batches)
//     and submitted.
//   - Manages models ("/models", "/model"), one-shot conversations
//     ("/ask" with optional model override and directory, reporting
//     the session ID), and one-off prompts to a chosen session
//     ("/say") without touching the chat binding.
//   - Manages sessions: "/sessions" renders a searchable HTML listing
//     across all known workspace directories ("/sessions <keyword>"
//     pre-filters), "/switch", "/new" (optionally in another
//     directory, transparently bringing up that workspace and its
//     event loop), "/info" (directory, skills, tools, context
//     watermark), "/export" (full conversation as HTML), "/summarize"
//     (manual compaction), "/status", "/cancel", "/help".
//   - Queues prompts while a session is busy; reconnects SSE streams
//     with backoff; optionally starts a `crush server` child process
//     when none is reachable ([Config.StartServer], default off) and
//     stops it on [Wrapper.Stop].
//   - Provides framework logging ([Logger], [SetLogger],
//     [SetLogLevel]) and a JSON file config ([LoadConfig],
//     [FileConfig.Apply]).
//
// # Host program
//
//	c, _ := client.DefaultClient("/repo")
//
//	cfg := imwrap.Config{Client: c, Adapter: myIMCLIAdapter{}, SelfAccount: "bot@im"}
//	if fc, err := imwrap.LoadConfig("imwrap.json"); err == nil {
//		fc.Apply(&cfg) // options only; Client/Adapter stay host-owned
//	}
//	imwrap.SetLogLevel(slog.LevelInfo) // shared logging
//
//	w, _ := imwrap.New(cfg)
//	if err := w.Start(ctx); err != nil { ... } // autostarts server when cfg.StartServer
//	defer w.Stop()
//
//	// From the IM listener: feed every message; the framework itself
//	// filters the bot's own output.
//	_ = w.HandleMessage(ctx, imwrap.IMMessage{ChatID: chat, ID: id, Sender: sender, Text: text})
//
//	// Custom commands extend the builtin set.
//	_ = w.RegisterCommand("deploy", "run the deploy pipeline",
//		func(ctx context.Context, cc imwrap.CommandContext) error {
//			return cc.Reply(ctx, "deploying "+cc.ArgText)
//		})
//
// The executable skeleton in examples/imhost shows a complete host
// built on an imaginary "im-cli".
package imwrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	crush "github.com/roberChen/crush-sdk"
	"github.com/roberChen/crush-sdk/config"
	"github.com/roberChen/crush-sdk/proto"
	"github.com/roberChen/crush-sdk/pubsub"
)

// DefaultSessionTitle is the title used for sessions the wrapper
// creates on behalf of a conversation.
const DefaultSessionTitle = "crush-im"

// Config configures a [Wrapper].
type Config struct {
	// Client is the crush-sdk client connected to a running
	// `crush server`. Required.
	Client *crush.Client

	// Adapter delivers text messages and files to the IM
	// conversations. Required.
	Adapter IMAdapter

	// CommandPrefix marks a message as a command. Defaults to "/".
	CommandPrefix string

	// SessionTitle is the title for sessions created by the
	// wrapper. Defaults to DefaultSessionTitle.
	SessionTitle string

	// DisableYOLO prevents the wrapper from creating workspaces with
	// the YOLO (auto-approve) flag. Permission requests then reach the
	// client and are auto-granted unless DisableAutoGrant is set too.
	DisableYOLO bool

	// DisableAutoGrant stops auto-granting permission requests that
	// arrive over SSE. With it unset (the default) the wrapper grants
	// every request, which is the YOLO behavior IM usage expects.
	DisableAutoGrant bool

	// GrantAction is the action used for auto-grants. Defaults to
	// proto.PermissionAllow (one-shot).
	GrantAction proto.PermissionAction

	// SelfAccount names the bot's own IM account (sender identifier).
	// Messages whose Sender equals it are treated as the bot's own
	// output and ignored. Use in the two-account scenario where user
	// and bot are separate accounts.
	SelfAccount string

	// EchoWindow bounds how long self-sent texts are remembered for
	// echo filtering in the shared-account scenario. Zero means 10
	// minutes; negative disables content echo matching entirely.
	EchoWindow time.Duration

	// DisableEchoFilter turns off all self-output filtering (message
	// IDs, sender account, and content echo).
	DisableEchoFilter bool

	// StartServer makes [Wrapper.Start] launch a `crush server`
	// child process when the server is not already reachable, like
	// `crush client` does. The child is terminated by [Wrapper.Stop].
	// Autostart only works with the default per-user socket (i.e. a
	// client built by crush.DefaultClient). Disabled by default.
	StartServer bool

	// ServerCommand is the command used for autostart. Defaults to
	// "crush" (invoked as `crush server`).
	ServerCommand string

	// ServerStartTimeout bounds how long Start waits for the spawned
	// server to become healthy. Zero means 15 seconds.
	ServerStartTimeout time.Duration

	// Logger is the slog logger the wrapper uses. Nil means the
	// package logger ([Logger]/[SetLogger]).
	Logger *slog.Logger
}

func (c *Config) setDefaults() {
	if c.CommandPrefix == "" {
		c.CommandPrefix = "/"
	}
	if c.SessionTitle == "" {
		c.SessionTitle = DefaultSessionTitle
	}
	if c.GrantAction == "" {
		c.GrantAction = proto.PermissionAllow
	}
	if c.ServerCommand == "" {
		c.ServerCommand = "crush"
	}
	if c.ServerStartTimeout <= 0 {
		c.ServerStartTimeout = 15 * time.Second
	}
}

// ErrNotStarted is returned by methods that need the event loop when
// the wrapper has not been started.
var ErrNotStarted = errors.New("imwrap: wrapper not started")

// Wrapper drives one or more Crush workspaces on behalf of IM
// conversations. Create with [New], start the event loops with
// [Wrapper.Start], and feed IM messages to [Wrapper.HandleMessage].
//
// The workspace for the client's path is the default; sessions can be
// created in other directories ([Wrapper.CreateSession], "/new -d"),
// which transparently brings up additional workspaces with their own
// SSE subscriptions.
type Wrapper struct {
	cfg     Config
	client  *crush.Client
	adapter IMAdapter
	log     *slog.Logger
	echo    *echoTracker

	wsID string // default workspace (client path)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	serverCmd *exec.Cmd // non-nil when autostart spawned a server

	mu         sync.Mutex
	chats      map[string]*chatState
	turns      map[string]*turnCollector
	cmds       map[string]*command
	workspaces map[string]string // absolute path -> workspace ID
	loops      map[string]bool   // workspace ID -> event loop running
	runs       map[string]*runState
}

// chatState is the per-IM-conversation binding. Guarded by Wrapper.mu.
type chatState struct {
	// wsID is the workspace owning sessionID.
	wsID string
	// sessionID is the session new prompts run on.
	sessionID string
	// activeSession/activeWS describe the in-flight (or last)
	// attached run; they differ from sessionID after /switch during
	// a run.
	activeSession string
	activeWS      string
	busy          bool
	runID         string
	queued        []string
	pendingQ      *pendingQuestion
}

// runState tracks one in-flight agent run, keyed by RunID. Attached
// runs own the chat's busy/queue slots; detached runs (one-shots,
// /say) never touch the chat binding.
type runState struct {
	runID         string
	chatID        string
	wsID          string
	sessionID     string
	prompt        string
	attached      bool
	reportSession bool
	// modelOverride marks that the run temporarily changed the
	// workspace default model; prevModel is what to restore.
	modelOverride bool
	prevModel     config.SelectedModel
}

// pendingQuestion is a question request surfaced to a chat, awaiting
// the user's reply.
type pendingQuestion struct {
	req    proto.QuestionRequest
	chatID string
	wsID   string
}

// New creates a [Wrapper] from the given config, applying defaults.
// Call [Wrapper.Start] to connect it to the server.
func New(cfg Config) (*Wrapper, error) {
	if cfg.Client == nil {
		return nil, errors.New("imwrap: Config.Client is required")
	}
	if cfg.Adapter == nil {
		return nil, errors.New("imwrap: Config.Adapter is required")
	}
	cfg.setDefaults()
	w := &Wrapper{
		cfg:     cfg,
		client:  cfg.Client,
		adapter: cfg.Adapter,
		log:     cfg.Logger,
		echo:    newEchoTracker(cfg.EchoWindow),
		chats:   make(map[string]*chatState),
		turns:   make(map[string]*turnCollector),
		cmds:    make(map[string]*command),
		loops:   make(map[string]bool),
		runs:    make(map[string]*runState),
	}
	if w.log == nil {
		w.log = Logger()
	}
	registerBuiltinCommands(w)
	return w, nil
}

// WorkspaceID returns the default workspace ID (the client's path),
// or "" before [Wrapper.Start] succeeds.
func (w *Wrapper) WorkspaceID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wsID
}

// Adapter returns the IM adapter the wrapper was created with, so
// host programs can reuse the same IM connection for their own
// messages.
func (w *Wrapper) Adapter() IMAdapter {
	return w.adapter
}

// Start resolves (or creates) the default workspace for the client's
// path, subscribes to its SSE event stream, and spawns the event
// loop. It returns once the subscription is established. Cancelling
// ctx stops all loops; [Wrapper.Stop] does the same and waits.
func (w *Wrapper) Start(ctx context.Context) error {
	if w.cfg.StartServer {
		if err := w.ensureServer(ctx); err != nil {
			return err
		}
	}
	wsID, err := w.resolveWorkspace(ctx, w.client.Path())
	if err != nil {
		return err
	}

	w.mu.Lock()
	if w.ctx != nil {
		w.mu.Unlock()
		return errors.New("imwrap: wrapper already started")
	}
	w.wsID = wsID
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.mu.Unlock()

	if err := w.ensureLoop(wsID); err != nil {
		w.Stop()
		return err
	}
	return nil
}

// Stop terminates all event loops and waits for in-flight handler
// goroutines. It also stops a server process spawned by autostart.
// It is safe to call more than once.
func (w *Wrapper) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.ctx = nil
	cmd := w.serverCmd
	w.serverCmd = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil {
			w.log.Warn("imwrap: failed to stop spawned server", "error", err)
			return
		}
		_ = cmd.Wait()
		w.log.Info("imwrap: spawned server stopped")
	}
}

// workspaceFor returns the workspace ID for a directory path,
// creating the workspace (and its event loop) when needed. The path
// is cleaned to its absolute form for matching.
func (w *Wrapper) workspaceFor(ctx context.Context, path string) (string, error) {
	wsID, err := w.resolveWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	return wsID, w.ensureLoop(wsID)
}

// resolveWorkspace finds or creates the workspace for a path without
// starting its event loop.
func (w *Wrapper) resolveWorkspace(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if err := validateWorkspaceDir(abs); err != nil {
		return "", err
	}

	w.mu.Lock()
	if id, ok := w.workspaces[abs]; ok {
		w.mu.Unlock()
		return id, nil
	}
	w.mu.Unlock()

	workspaces, err := w.client.ListWorkspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if filepath.Clean(ws.Path) == abs {
			w.rememberWorkspace(abs, ws.ID)
			return ws.ID, nil
		}
	}
	created, err := w.client.CreateWorkspace(ctx, proto.Workspace{
		Path: abs,
		YOLO: !w.cfg.DisableYOLO,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create workspace: %w", err)
	}
	w.rememberWorkspace(abs, created.ID)
	return created.ID, nil
}

// validateWorkspaceDir checks that a workspace path exists and is a
// directory before any session is bound to it, so typos in "/new -d"
// or AskOptions.Dir fail fast with a clear message instead of
// creating a workspace in a bogus location.
func validateWorkspaceDir(abs string) error {
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("目录不可用 %s: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("路径不是目录: %s", abs)
	}
	return nil
}

func (w *Wrapper) rememberWorkspace(path, wsID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.workspaces == nil {
		w.workspaces = make(map[string]string)
	}
	w.workspaces[path] = wsID
}

// pathForWS returns the directory path bound to a workspace ID, or
// "" when unknown.
func (w *Wrapper) pathForWS(wsID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for path, id := range w.workspaces {
		if id == wsID {
			return path
		}
	}
	return ""
}

// workspaceIDs snapshots the known workspace IDs (default first).
func (w *Wrapper) workspaceIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := []string{}
	if w.wsID != "" {
		ids = append(ids, w.wsID)
	}
	for _, id := range w.workspaces {
		if !containsID(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// chatWorkspace returns the workspace a chat is currently bound to
// (the workspace of its session), falling back to the default.
func (w *Wrapper) chatWorkspace(chatID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if st, ok := w.chats[chatID]; ok && st.wsID != "" {
		return st.wsID
	}
	return w.wsID
}

// ensureLoop starts an SSE event loop for a workspace exactly once.
func (w *Wrapper) ensureLoop(wsID string) error {
	w.mu.Lock()
	if w.loops[wsID] {
		w.mu.Unlock()
		return nil
	}
	if w.ctx == nil {
		w.mu.Unlock()
		return ErrNotStarted
	}
	w.loops[wsID] = true
	w.mu.Unlock()

	events, err := w.client.SubscribeEvents(w.ctx, wsID)
	if err != nil {
		w.mu.Lock()
		delete(w.loops, wsID)
		w.mu.Unlock()
		return fmt.Errorf("failed to subscribe to workspace events: %w", err)
	}
	w.wg.Go(func() {
		defer w.loopDone(wsID)
		w.eventLoop(wsID, events)
	})
	return nil
}

// loopDone clears the loop marker so a later call can restart it.
func (w *Wrapper) loopDone(wsID string) {
	w.mu.Lock()
	delete(w.loops, wsID)
	w.mu.Unlock()
}

// runCtx returns a context tied to the wrapper lifetime for
// background goroutines.
func (w *Wrapper) runCtx() (context.Context, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx == nil {
		return nil, ErrNotStarted
	}
	return w.ctx, nil
}

// eventLoop consumes one workspace's SSE stream, reconnecting with
// backoff until the wrapper is stopped.
func (w *Wrapper) eventLoop(wsID string, events <-chan any) {
	for {
		w.dispatchEvents(wsID, events)
		ctx, err := w.runCtx()
		if err != nil || ctx.Err() != nil {
			return
		}
		delay := time.Second
		next, subErr := w.client.SubscribeEvents(ctx, wsID)
		if subErr != nil {
			w.log.Error("imwrap: resubscribing to events failed", "workspace", wsID, "error", subErr)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			continue
		}
		events = next
	}
}

// dispatchEvents handles one subscription's stream until it closes.
func (w *Wrapper) dispatchEvents(wsID string, events <-chan any) {
	for ev := range events {
		switch e := ev.(type) {
		case pubsub.Event[proto.PermissionRequest]:
			w.onPermissionRequest(wsID, e.Payload)
		case pubsub.Event[proto.QuestionRequest]:
			w.onQuestionRequest(wsID, e.Payload)
		case pubsub.Event[proto.QuestionNotification]:
			w.onQuestionNotification(e.Payload)
		case pubsub.Event[proto.Message]:
			w.onMessageEvent(e.Payload)
		case pubsub.Event[proto.RunComplete]:
			w.onRunComplete(e.Payload)
		}
	}
}

// onPermissionRequest auto-grants the request when configured. The
// grant runs on its own goroutine so a slow IM or server call cannot
// stall the event loop while the agent waits.
func (w *Wrapper) onPermissionRequest(wsID string, req proto.PermissionRequest) {
	if w.cfg.DisableAutoGrant {
		return
	}
	w.wg.Go(func() {
		ctx, err := w.runCtx()
		if err != nil {
			return
		}
		resolved, err := w.client.GrantPermission(ctx, wsID, proto.PermissionGrant{
			Permission: req,
			Action:     w.cfg.GrantAction,
		})
		switch {
		case err != nil:
			w.log.Warn("imwrap: auto-grant failed", "tool", req.ToolName, "error", err)
		case !resolved:
			// Another subscriber resolved it first; not an error.
		default:
			w.log.Debug("imwrap: auto-granted permission", "tool", req.ToolName)
		}
	})
}

// onMessageEvent folds a created/updated message into the turn
// collector for its session, if any.
func (w *Wrapper) onMessageEvent(msg proto.Message) {
	if msg.SessionID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.turns[msg.SessionID]; ok {
		t.apply(msg)
	}
}

// state returns (creating if needed) the chat state for chatID.
// Caller must hold Wrapper.mu.
func (w *Wrapper) state(chatID string) *chatState {
	st, ok := w.chats[chatID]
	if !ok {
		st = &chatState{}
		w.chats[chatID] = st
	}
	return st
}

// turnFor returns (creating if needed) the turn collector for a
// session. Caller must hold Wrapper.mu.
func (w *Wrapper) turnFor(sessionID string) *turnCollector {
	t, ok := w.turns[sessionID]
	if !ok {
		t = newTurnCollector()
		w.turns[sessionID] = t
	}
	return t
}

// resetTurn drops any previous turn data for a session before a new
// run starts. Caller must hold Wrapper.mu.
func (w *Wrapper) resetTurn(sessionID string) {
	w.turns[sessionID] = newTurnCollector()
}

// registerRun records a runState and seeds its turn collector.
// Caller must hold Wrapper.mu.
func (w *Wrapper) registerRun(run *runState, runID string) {
	run.runID = runID
	w.runs[runID] = run
	w.resetTurn(run.sessionID)
	w.turnFor(run.sessionID).setPrompt(run.prompt)
}

// dropRun removes a run registration (completion or failure).
func (w *Wrapper) dropRun(runID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.runs, runID)
}

// newRunID mints a correlation ID for one agent turn.
func newRunID() string {
	return uuid.NewString()
}
