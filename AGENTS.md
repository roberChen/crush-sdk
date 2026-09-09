# AGENTS.md

Go client SDK for Crush's client-server mode. This module was extracted from
`internal/client` of [charmbracelet/crush](https://github.com/charmbracelet/crush)
so third-party programs (editors, scripts, custom TUIs, automation agents) can
drive a `crush server` over the same JSON-over-HTTP protocol the official TUI
uses.

- Module: `github.com/roberChen/crush-sdk` (import path), package name `client`
- Go 1.27+ required (`go.mod` says `go 1.27.0`)
- No Makefile, no CI workflows, no linter config. Standard `go` toolchain only.
- `.crush/` at the repo root is Crush's own runtime state (session DB, logs) and
  is gitignored; it is not part of the SDK.

## Commands

```sh
go build ./...        # build everything
go test ./...         # run all tests (uses testify require, t.Parallel())
go vet ./...          # must stay clean
gofmt -l .            # must be empty
```

Run the end-to-end example against a live server (requires a running
`crush server`):

```sh
go run ./examples/e2e [-host unix:///path/to.sock] [-path /project/dir]
```

The imwrap host-program skeleton (imaginary `im-cli` commands, no server
needed to compile) lives in `examples/imhost`.

## Layout

| Package | Contents |
| --- | --- |
| root (`client`) | `Client` type + all RPC methods, transport, error sentinels |
| `proto/` | Wire types: messages, workspaces, sessions, events, permissions, skills, tools, MCP |
| `config/` | Wire-compatible mirror of upstream `internal/config`, trimmed to types the client exchanges |
| `oauth/` | OAuth2 token types (`Token`, `OAuthClient`, `TokenExchangeError`) |
| `pubsub/` | SSE envelope (`Payload`/`Event`) types + an in-process fan-out `Broker` |
| `message/` | `Attachment` type used by `Client.SendMessage` |
| `tools/` | Tool-name constants + permission-parameter structs |
| `lsp/` | LSP `ServerState` constants |
| `csync/` | Concurrent data structures used by `config.Config` |
| `imwrap/` | Chat-oriented wrapper driving Crush from IM software (see below) |

Root package files are split by domain:
`client.go` (construction, transport, server lifecycle), `proto.go` (most
workspace/session/agent RPCs), `config.go` (config-related RPCs), `host.go`
(default socket discovery), `errors.go` (sentinels + `checkStatus`),
`dial_other.go`/`dial_windows.go` (build-tagged pipe dialer).

## Transport internals (non-obvious)

- Every request is built by `sendReq` (client.go), which prefixes paths with
  `/v1`. RPC methods pass paths like `"/workspaces"`.
- The HTTP transport dials through `Client.dialer`, which switches on
  `c.network`: `unix` → `net.Dialer` on `c.addr`, `npipe` → `winio.DialPipeContext`
  (Windows only; the non-Windows stub returns `syscall.EAFNOSUPPORT`), default →
  plain TCP.
- For unix/npipe, `r.Host` is set to `DummyHost` (`api.crush.localhost`,
  client.go) so `http.Transport` does not try to resolve the fake host; the
  real target is `c.addr`. HTTP/1.1 is forced and compression disabled for
  socket transports.
- `Client.h` has `Timeout: 0`. There is no client-side request deadline;
  callers are expected to control timeouts via `context.Context`. Dial timeout
  is 30s.
- `NewClient` mints a per-process UUID `clientID`, sent as a `client_id` query
  parameter on workspace-scoped calls and to `/clients/{id}`. `RetireClient`
  is authoritative: after it succeeds the server refuses further workspace
  creates from that client ID.
- `Client` is safe for concurrent use; a fresh `Client` per process is the
  intended model.

## Error handling

- Typed sentinels in errors.go: `ErrNotFound` (404), `ErrServerShuttingDown`
  (503), `ErrServerBusy` (409 on shutdown), `ErrUnsupported` (server predates
  the feature; caller must decide fallback). Match with `errors.Is`; the
  sentinels are wrapped with `%w`.
- `checkStatus(rsp, ok...)` validates status and decodes the body as
  `proto.Error{Message}` when present. **Not all methods use it** — many older
  methods inline `if rsp.StatusCode != http.StatusOK` instead. When adding a
  new method, follow the pattern of adjacent code; do not refactor existing
  methods to `checkStatus` casually, as error-string shape is asserted in
  tests.
- Error wrapping style: `fmt.Errorf("failed to <action>: %w", err)`. On success
  but a non-OK status without a body, `fmt.Errorf("failed to <action>: status code %d", ...)`.
- `jsonBody` ignores marshal errors (proto.go) — it cannot fail for the struct
  types used in practice, but don't pass types with custom marshalers that can
  error.

## SSE event stream

- `SubscribeEvents(ctx, workspaceID)` returns `<-chan any` (buffered 100). Each
  element is a `pubsub.Event[T]` with concrete payload types from `proto`
  (e.g. `pubsub.Event[proto.Message]`). Type-assert on the event value.
- The stream is a plain `data:` line SSE loop; the outer envelope is
  `pubsub.Payload{Type, Payload}` where `Payload` is raw JSON discriminated by
  `Type` (`pubsub.PayloadType*` constants in pubsub/events.go). Unknown types
  are logged and skipped — this is what makes the SDK forward-compatible with
  newer servers.
- **Completion correlation**: `SendMessage(..., runID, ...)` echoes `runID` on
  the `RunComplete` event (`pubsub.PayloadTypeRunComplete`). Filtering by
  `SessionID` alone is only safe when no other turn is in flight for that
  session; always set a fresh RunID in automation.
- The channel closes when the context is cancelled or the stream ends.

## Wire compatibility with upstream

This code is a vendored/extracted subset of `charmbracelet/crush`. Several
places carry explicit sync obligations — respect them when changing types:

- `proto/skills.go`: `SkillDiscoveryState` values "must stay in sync with
  `internal/skills.DiscoveryState`; do not reorder without a coordinated
  server/client bump."
- `tools/permissions.go`: tool names must match the server's tool registry.
- `lsp/state.go`: mirrors `internal/lsp` wire definitions.
- Forward compatibility is a design goal: unknown JSON fields must be ignored
  (never reject on extra fields), and unknown SSE payload types must be
  skipped, not fatal.
- `proto/session.go`, `proto/proto.go` doc comments reference server-side
  behavior (`internal/server/proto.go`, agent coordinator) — read them before
  changing fields with computed-on-read semantics like `Session.IsBusy` and
  `AttachedClients` (they are populated by REST handlers, not persisted).

## Non-obvious type conventions

- JSON `any` fields that must survive the wire get explicit kind tags:
  `SetProviderAPIKey` accepts `string` or `*oauth.Token` and encodes with
  `proto.APIKeyKind` (`ConfigProviderKeyRequest.Kind` + raw JSON); decode via
  `DecodeAPIKey()`. Follow this pattern for new polymorphic fields.
- `proto.PermissionRequest.Params` is `any` with a custom `UnmarshalJSON` that
  dispatches on `ToolName` via `unmarshalToolParams` (proto/tools.go); unknown
  tools must be handled gracefully there.
- `config.Config` stores maps as `csync.Map[string, any]` (custom
  `MarshalJSON`/`UnmarshalJSON`). `csync.Value` panics on pointer/slice/map
  types by design — use `csync.Map`/`csync.Slice` for those.
- `proto.MCPState` marshals as text with a space in `"needs auth"`.
- `proto.Message` has extensive helper methods (`Content()`, `ToolCalls()`,
  `IsFinished()`, append helpers) — use them rather than poking `Parts`.

## imwrap package

`imwrap` is original code (not extracted from upstream): a chat framework over
`Client` for IM integrations. Hosts provide `IMAdapter` plus custom commands;
everything else is framework-owned. Key invariants:

- Self-output filtering runs first in `HandleMessage` (message-ID marks,
  `Config.SelfAccount`, then content echo via `echoTracker`; all wrapper sends
  go through `sendText` so they are recorded). `SentAt` guards content matches
  against pre-recording user messages. `DisableEchoFilter` bypasses everything.
- Logging is framework-managed (`log.go`): the wrapper uses `w.log`
  (Config.Logger or the package logger); hosts use `imwrap.Logger()`.
- Server autostart (`server.go`) is opt-in (`Config.StartServer`): Health is
  probed first; a `crush server` child is spawned only when unreachable, its
  output piped into the wrapper logger, and `Stop()` kills it.
- File config (`config.go`): `FileConfig` (JSON) -> `Apply(&Config)`; it never
  provides Client/Adapter, only options (plus `log_file` for file logging and
  the host-reserved `extra` section for program config takeover). Unknown log
  levels fall back to info. The two logger tests mutate the package logger and
  must stay sequential (no t.Parallel).
- Session reports and `/git` exports end with a sidebar-like footer
  (`footer.go`: title, dir, model, context usage bar, git branch via local git
  exec). Workspace dirs are validated (exists, is a dir) in resolveWorkspace.
- Command listing folds aliases into their primary entry (two-pass: primaries
  first, then aliases; map order is random). Builtin registration is
  defensive: duplicates log a warning and are skipped, never overwriting and
  never panicking; RegisterCommand cannot override builtins.
- Turn correlation uses a fresh RunID per `SendMessage`; runs are tracked in
  `Wrapper.runs` keyed by RunID (`runState`). Attached runs own the chat's
  busy/queue slots; detached runs (`/ask` one-shots, `/say`) never touch the
  chat binding. Message events fold into per-session `turnCollector`s under
  `Wrapper.mu`. Sub-agent (`task`/`agent` tool) sessions are never bound to a
  chat; their transcripts are only fetched (ListSessions by ParentSessionID,
  filtered by turn start time) to nest inside the HTML report.
- Multi-workspace: sessions can live in other directories (`/new -d`,
  `AskOptions.Dir`); `workspaceFor` resolves/creates a workspace per absolute
  path and `ensureLoop` runs one SSE event loop per workspace. Event handlers
  receive the originating wsID (permissions/questions are workspace-scoped).
- Config bools are opt-out (`DisableYOLO`, `DisableAutoGrant`) because the
  wanted defaults (YOLO on, auto-grant on) are not the zero value.
- Permission requests are auto-granted on their own goroutine; RunComplete and
  Question handling also dispatch goroutines (via `wg.Go`). Every accepted
  prompt acks immediately (`ackRun`).
- Model overrides in `AskOnce` temporarily change the workspace default model
  (scope workspace, type large) and restore it on completion/failure;
  concurrent runs in the same workspace may observe the override (documented).
- HTML rendering (`html.go`, `markdown.go`, `diff.go`) is dependency-free and
  escape-first: raw HTML never passes through. Markdown covers blocks
  (headings, tables with alignment, lists, quotes, hr, fences) and inline
  markup (bold, em, strike, code, links, images-as-links, guarded autolinks).
  Tool calls render together with their results (indexToolResults by
  tool_call_id; orphan results still render standalone). `edit`/`write`/
  `multiedit` render LCS diffs (capped at `maxDiffLines`); `task`/`agent`
  render input, nested child-session transcript, and labeled 输出. The
  /sessions listing renders as a self-contained HTML file with an inline
  client-side search box (`sessionshtml.go`); `/sessions <keyword>` also
  filters server-side before rendering.
- Tests use a fake HTTP server (`fakeserver_test.go`) that speaks the /v1
  protocol including a pushable SSE stream, multi-workspace stores, and
  config/model/summarize endpoints; async effects are awaited with `waitFor`.
  Keep new event-flow logic covered there.

## Testing

- Testify `require` only (no `assert`), `t.Parallel()` on all tests, table
  tests where applicable.
- `captureClient(t, srv)` (config_test.go) builds a TCP `Client` pointed at an
  `httptest.Server` — the standard way to unit-test RPC methods; assert on
  `r.URL.Path` (including the `/v1` prefix) and request body.
- `host_test.go` and `dial_windows.go` are build-tagged (`//go:build !windows`
  / `windows`). When testing host/address logic that is platform-specific,
  tag accordingly. `dial_windows.go` still carries a legacy `// +build windows`
  line; gopls flags it as unnecessary — harmless, leave it or remove it, but
  don't cargo-cult it into new files.

## Style

- All exported symbols have doc comments (golint convention); many doc comments
  carry real protocol semantics — read them.
- RPC method names follow domain prefixes: `List*`, `Get*`, `Create*`,
  `Delete*`, `MCP*`, `LSP*`, `Agent*`, `FileTracker*`. Workspace ID is the
  first parameter after `ctx`, conventionally named `id`.
- Request/response body structs live in `proto/requests.go` (or inline
  anonymous structs in the RPC method for one-off bodies — both patterns
  exist).
- No em dashes in source; comments use commas/parentheses/semicolons.

## License

FSL-1.1-MIT (see LICENSE.md), © Charmbracelet, Inc. for upstream-derived code.
When porting more code from upstream, keep attribution and the "mirrors
<upstream path>" doc comments.
