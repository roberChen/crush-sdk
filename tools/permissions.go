// Package tools contains the tool names and permission parameter types
// shared between the Crush client and server. It mirrors the wire
// definitions of github.com/charmbracelet/crush/internal/agent/tools.
package tools

// Tool name constants. Keep in sync with the server's tool registry.
const (
	AgenticFetchToolName = "agentic_fetch"
	BashToolName         = "bash"
	DownloadToolName     = "download"
	EditToolName         = "edit"
	FetchToolName        = "fetch"
	LSToolName           = "ls"
	MultiEditToolName    = "multiedit"
	ViewToolName         = "view"
	WriteToolName        = "write"
)

// PermissionsParams types. These mirror the wire format of the
// corresponding server-side definitions so permission payloads
// round-trip unchanged.

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
}

type DownloadPermissionsParams struct {
	URL      string `json:"url"`
	FilePath string `json:"file_path"`
	Timeout  int    `json:"timeout,omitempty"`
}

type EditPermissionsParams struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type AgenticFetchPermissionsParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

type FetchPermissionsParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}

type LSPermissionsParams struct {
	Path   string   `json:"path"`
	Ignore []string `json:"ignore"`
	Depth  int      `json:"depth"`
}

type MultiEditPermissionsParams struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type ViewPermissionsParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

type WritePermissionsParams struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}
