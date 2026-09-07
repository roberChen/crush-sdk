// Package config contains the configuration types shared between the
// Crush client and server. It is a wire-compatible mirror of
// github.com/charmbracelet/crush/internal/config, trimmed to the types
// the client API actually exchanges over HTTP.
package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"

	"github.com/roberChen/crush-sdk/csync"
	"github.com/roberChen/crush-sdk/oauth"
)

// Scope determines which config file is targeted for read/write operations.
type Scope int

const (
	// ScopeGlobal targets the global data config (~/.local/share/crush/crush.json).
	ScopeGlobal Scope = iota
	// ScopeWorkspace targets the workspace config (.crush/crush.json).
	ScopeWorkspace
)

// String returns a human-readable label for the scope.
func (s Scope) String() string {
	switch s {
	case ScopeGlobal:
		return "global"
	case ScopeWorkspace:
		return "workspace"
	default:
		return fmt.Sprintf("Scope(%d)", int(s))
	}
}

type SelectedModelType string

// String returns the string representation of the SelectedModelType.
func (s SelectedModelType) String() string {
	return string(s)
}

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

const (
	AgentCoder string = "coder"
	AgentTask  string = "task"
)

type SelectedModel struct {
	// The model id as used by the provider API.
	Model string `json:"model"`
	// The model provider, same as the key/id used in the providers config.
	Provider string `json:"provider"`

	// Only used by models that use the openai provider and need this set.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Used by anthropic models that can reason to indicate if the model should think.
	Think bool `json:"think,omitempty"`

	// Overrides the default model configuration.
	MaxTokens        int64    `json:"max_tokens,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	TopK             *int64   `json:"top_k,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`

	// Override provider specific options.
	ProviderOptions map[string]any `json:"provider_options,omitempty"`
}

type ProviderConfig struct {
	// The provider's id.
	ID string `json:"id,omitempty"`
	// The provider's name, used for display purposes.
	Name string `json:"name,omitempty"`
	// The provider's API endpoint.
	BaseURL string `json:"base_url,omitempty"`
	// The provider type, e.g. "openai", "anthropic", etc. if empty it defaults to openai.
	Type catwalk.Type `json:"type,omitempty"`
	// The provider's API key.
	APIKey string `json:"api_key,omitempty"`
	// The original API key template before resolution (for re-resolution on auth errors).
	APIKeyTemplate string `json:"-"`
	// OAuthToken for providers that use OAuth2 authentication.
	OAuthToken *oauth.Token `json:"oauth,omitempty"`
	// Marks the provider as disabled.
	Disable bool `json:"disable,omitempty"`

	// Custom system prompt prefix.
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty"`

	// Extra headers to send with each request to the provider.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`

	// ExtraBody is merged verbatim into OpenAI-compatible request bodies.
	ExtraBody map[string]any `json:"extra_body,omitempty"`

	ProviderOptions map[string]any `json:"provider_options,omitempty"`

	// Used to pass extra parameters to the provider.
	ExtraParams map[string]string `json:"-"`

	// AWSAuthRefresh is a shell command run when Bedrock returns a
	// credential error.
	AWSAuthRefresh string `json:"aws_auth_refresh,omitempty"`

	// Skip cost accumulation for this provider when using subscription or flat rate billing.
	FlatRate bool `json:"flat_rate,omitempty"`

	// AutoDiscoverModels controls model discovery via /v1/models endpoint.
	AutoDiscoverModels *bool `json:"discover_models,omitempty"`

	// The provider models.
	Models []catwalk.Model `json:"models,omitempty"`
}

type MCPType string

const (
	MCPStdio MCPType = "stdio"
	MCPSSE   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

type MCPConfig struct {
	Command       string            `json:"command,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Type          MCPType           `json:"type"`
	URL           string            `json:"url,omitempty"`
	Disabled      bool              `json:"disabled,omitempty"`
	DisabledTools []string          `json:"disabled_tools,omitempty"`
	EnabledTools  []string          `json:"enabled_tools,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`

	// Sessionless marks a server that does not maintain an MCP session.
	Sessionless *bool `json:"sessionless,omitempty"`

	// Headers are HTTP headers for HTTP/SSE MCP servers.
	Headers map[string]string `json:"headers,omitempty"`

	// OAuth enables the MCP OAuth 2.1 authorization flow for HTTP
	// transport servers.
	OAuth bool `json:"oauth,omitempty"`

	// OAuthClientID is an optional pre-registered OAuth client ID.
	OAuthClientID string `json:"oauth_client_id,omitempty"`

	// OAuthClientSecret is the optional secret paired with OAuthClientID.
	OAuthClientSecret string `json:"oauth_client_secret,omitempty"`

	// OAuthCallbackPort pins the localhost port used for the OAuth
	// redirect listener.
	OAuthCallbackPort int `json:"oauth_callback_port,omitempty"`

	// OAuthToken is the persisted OAuth token for this server.
	OAuthToken *oauth.Token `json:"oauth_token,omitempty"`
}

type LSPConfig struct {
	Disabled    bool              `json:"disabled,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	FileTypes   []string          `json:"filetypes,omitempty"`
	RootMarkers []string          `json:"root_markers,omitempty"`
	InitOptions map[string]any    `json:"init_options,omitempty"`
	Options     map[string]any    `json:"options,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
}

type TUIOptions struct {
	CompactMode bool   `json:"compact_mode,omitempty"`
	DiffMode    string `json:"diff_mode,omitempty"`

	Completions Completions `json:"completions,omitzero"`
	Transparent *bool       `json:"transparent,omitempty"`
	Scrollbar   string      `json:"scrollbar,omitempty"`
	ExitBanner  ExitBanner  `json:"exit_banner,omitempty"`
}

// Completions defines options for the completions UI.
type Completions struct {
	MaxDepth *int `json:"max_depth,omitempty"`
	MaxItems *int `json:"max_items,omitempty"`
}

// Diff mode options.
const (
	DiffModeUnified = "unified"
	DiffModeSplit   = "split"
)

// Scrollbar visibility options.
const (
	ScrollbarDefault = "default"
	ScrollbarAlways  = "always"
	ScrollbarNever   = "never"
)

// ExitBanner selects what Crush prints after the TUI exits.
type ExitBanner string

const (
	ExitBannerDefault ExitBanner = "default"
	ExitBannerCompact ExitBanner = "compact"
	ExitBannerNone    ExitBanner = "none"
)

type Permissions struct {
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

type TrailerStyle string

const (
	TrailerStyleNone         TrailerStyle = "none"
	TrailerStyleCoAuthoredBy TrailerStyle = "co-authored-by"
	TrailerStyleAssistedBy   TrailerStyle = "assisted-by"
)

type Attribution struct {
	TrailerStyle  TrailerStyle `json:"trailer_style,omitempty"`
	CoAuthoredBy  *bool        `json:"co_authored_by,omitempty"`
	GeneratedWith bool         `json:"generated_with,omitempty"`
}

type Options struct {
	ContextPaths         []string    `json:"context_paths,omitempty"`
	GlobalContextPaths   []string    `json:"global_context_paths,omitempty"`
	SkillsPaths          []string    `json:"skills_paths,omitempty"`
	TUI                  *TUIOptions `json:"tui,omitempty"`
	Debug                bool        `json:"debug,omitempty"`
	DebugLSP             bool        `json:"debug_lsp,omitempty"`
	DisableAutoSummarize bool        `json:"disable_auto_summarize,omitempty"`
	// DataDirectory is where Crush keeps per-project state such as the
	// SQLite database and workspace overrides.
	DataDirectory             string       `json:"data_directory,omitempty"`
	DisabledTools             []string     `json:"disabled_tools,omitempty"`
	DisableProviderAutoUpdate bool         `json:"disable_provider_auto_update,omitempty"`
	DisableDefaultProviders   bool         `json:"disable_default_providers,omitempty"`
	Attribution               *Attribution `json:"attribution,omitempty"`
	DisableMetrics            bool         `json:"disable_metrics,omitempty"`
	InitializeAs              string       `json:"initialize_as,omitempty"`
	AutoLSP                   *bool        `json:"auto_lsp,omitempty"`
	Progress                  *bool        `json:"progress,omitempty"`
	Notifications             string       `json:"notifications,omitempty"`
	DisabledSkills            []string     `json:"disabled_skills,omitempty"`
	RequestTimeout            *int         `json:"request_timeout,omitempty"`
}

// DefaultRequestTimeout bounds each LLM API request when the user has
// not configured a timeout.
const DefaultRequestTimeout = time.Minute

// GetRequestTimeout returns the per-request timeout for LLM API calls.
func (o *Options) GetRequestTimeout() time.Duration {
	if o == nil || o.RequestTimeout == nil {
		return DefaultRequestTimeout
	}
	if *o.RequestTimeout <= 0 {
		return 0
	}
	return time.Duration(*o.RequestTimeout) * time.Second
}

type MCPs map[string]MCPConfig

type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

// Sorted returns the MCP entries sorted by name.
func (m MCPs) Sorted() []MCP {
	sorted := make([]MCP, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

// Sorted returns the LSP entries sorted by name.
func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// This is the id of the system prompt used by the agent.
	Disabled bool `json:"disabled,omitempty"`

	Model SelectedModelType `json:"model"`

	// The available tools for the agent. If nil, all tools are available.
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// Which MCPs (and which of their tools) are available for this agent.
	AllowedMCP map[string][]string `json:"allowed_mcp,omitempty"`

	// Overrides the context paths for this agent.
	ContextPaths []string `json:"context_paths,omitempty"`
}

type Tools struct {
	Ls   ToolLs   `json:"ls,omitzero"`
	Grep ToolGrep `json:"grep,omitzero"`
	Glob ToolGlob `json:"glob,omitzero"`
}

type ToolLs struct {
	MaxDepth *int `json:"max_depth,omitempty"`
	MaxItems *int `json:"max_items,omitempty"`
}

// Limits returns the user-defined max-depth and max-items, or their defaults.
func (t ToolLs) Limits() (depth, items int) {
	return ptrValOr(t.MaxDepth, 0), ptrValOr(t.MaxItems, 0)
}

type ToolGrep struct {
	Timeout *time.Duration `json:"timeout,omitempty"`
}

// GetTimeout returns the user-defined timeout or the default.
func (t ToolGrep) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 5*time.Second)
}

type ToolGlob struct {
	Timeout *time.Duration `json:"timeout,omitempty"`
}

// GetTimeout returns the user-defined timeout or the default.
func (t ToolGlob) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 30*time.Second)
}

// HookConfig defines a user-configured shell command that fires on a
// hook event (e.g. PreToolUse). This is a pure-data struct.
type HookConfig struct {
	// Friendly display name shown in the TUI. Falls back to Command when empty.
	Name string `json:"name,omitempty"`
	// Regex pattern tested against the tool name. Empty means match all.
	Matcher string `json:"matcher,omitempty"`
	// Shell command to execute.
	Command string `json:"command"`
	// Timeout in seconds. Default 30.
	Timeout int `json:"timeout,omitempty"`
}

// DisplayName returns the hook name for display purposes.
func (h *HookConfig) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Command
}

// TimeoutDuration returns the hook timeout as a time.Duration, defaulting
// to 30s.
func (h *HookConfig) TimeoutDuration() time.Duration {
	if h.Timeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(h.Timeout) * time.Second
}

// Config holds the configuration for crush.
type Config struct {
	Schema string `json:"$schema,omitempty"`

	// We currently only support large/small as values here.
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty"`

	// Recently used models stored in the data directory config.
	RecentModels map[SelectedModelType][]SelectedModel `json:"recent_models,omitempty"`

	// The providers that are configured.
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty"`

	MCP MCPs `json:"mcp,omitempty"`

	LSP LSPs `json:"lsp,omitempty"`

	Options *Options `json:"options,omitempty"`

	Permissions *Permissions `json:"permissions,omitempty"`

	Tools Tools `json:"tools,omitzero"`

	Hooks map[string][]HookConfig `json:"hooks,omitempty"`

	// Env is a map of environment variables set on startup.
	Env map[string]string `json:"env,omitempty"`

	Agents map[string]Agent `json:"-"`
}

func ptrValOr[T any](v *T, def T) T {
	if v == nil {
		return def
	}
	return *v
}
