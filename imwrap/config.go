package imwrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/roberChen/crush-sdk/proto"
)

// FileConfig is the JSON file representation of the wrapper options
// that make sense to externalize (everything except the Client and
// Adapter handles a program must provide). It exists so deployments
// can tune behavior without recompiling.
//
// Example imwrap.json:
//
//	{
//	  "command_prefix": "/",
//	  "session_title": "crush-im",
//	  "self_account": "bot@im",
//	  "echo_window_seconds": 600,
//	  "start_server": false,
//	  "server_command": "crush",
//	  "server_start_timeout_seconds": 15,
//	  "log_level": "info"
//	}
type FileConfig struct {
	// CommandPrefix marks commands in IM messages. Empty means "/".
	CommandPrefix string `json:"command_prefix,omitempty"`
	// SessionTitle titles wrapper-created sessions.
	SessionTitle string `json:"session_title,omitempty"`
	// SelfAccount is the bot's own IM account; its messages are
	// treated as bot output (two-account scenario).
	SelfAccount string `json:"self_account,omitempty"`
	// EchoWindowSeconds bounds self-output content matching for the
	// shared-account scenario. Negative disables content matching.
	EchoWindowSeconds int `json:"echo_window_seconds,omitempty"`
	// DisableEchoFilter turns off all self-output filtering.
	DisableEchoFilter bool `json:"disable_echo_filter,omitempty"`
	// DisableYOLO creates workspaces without the auto-approve flag.
	DisableYOLO bool `json:"disable_yolo,omitempty"`
	// DisableAutoGrant stops auto-granting SSE permission requests.
	DisableAutoGrant bool `json:"disable_auto_grant,omitempty"`
	// GrantAction selects the auto-grant action: "allow" (default)
	// or "allow_session".
	GrantAction string `json:"grant_action,omitempty"`
	// StartServer autostarts a `crush server` child when unreachable.
	StartServer bool `json:"start_server,omitempty"`
	// ServerCommand is the autostart command; default "crush".
	ServerCommand string `json:"server_command,omitempty"`
	// ServerStartTimeoutSeconds bounds waiting for autostart.
	ServerStartTimeoutSeconds int `json:"server_start_timeout_seconds,omitempty"`
	// LogLevel is one of debug, info, warn, error; applied to the
	// package logger.
	LogLevel string `json:"log_level,omitempty"`
}

// LoadConfig reads the first existing file among paths. It returns a
// zero FileConfig with an os.ErrNotExist-wrapped error when none
// exist, so callers can treat "no config file" as "use defaults".
func LoadConfig(paths ...string) (FileConfig, error) {
	var cfg FileConfig
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return cfg, fmt.Errorf("failed to read config %s: %w", p, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("failed to parse config %s: %w", p, err)
		}
		return cfg, nil
	}
	return cfg, fmt.Errorf("no config file found among %v", paths)
}

// Apply folds the file options into a Config. Values left empty in
// the file keep the Config's existing (or default) values.
func (f FileConfig) Apply(cfg *Config) {
	if f.CommandPrefix != "" {
		cfg.CommandPrefix = f.CommandPrefix
	}
	if f.SessionTitle != "" {
		cfg.SessionTitle = f.SessionTitle
	}
	if f.SelfAccount != "" {
		cfg.SelfAccount = f.SelfAccount
	}
	if f.EchoWindowSeconds != 0 {
		cfg.EchoWindow = time.Duration(f.EchoWindowSeconds) * time.Second
	}
	cfg.DisableEchoFilter = cfg.DisableEchoFilter || f.DisableEchoFilter
	cfg.DisableYOLO = cfg.DisableYOLO || f.DisableYOLO
	cfg.DisableAutoGrant = cfg.DisableAutoGrant || f.DisableAutoGrant
	if f.GrantAction != "" {
		cfg.GrantAction = proto.PermissionAction(f.GrantAction)
	}
	cfg.StartServer = cfg.StartServer || f.StartServer
	if f.ServerCommand != "" {
		cfg.ServerCommand = f.ServerCommand
	}
	if f.ServerStartTimeoutSeconds > 0 {
		cfg.ServerStartTimeout = time.Duration(f.ServerStartTimeoutSeconds) * time.Second
	}
	if f.LogLevel != "" {
		SetLogLevel(parseLogLevel(f.LogLevel))
	}
}

// parseLogLevel maps a config string to a slog level, defaulting to
// info for unknown values.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
