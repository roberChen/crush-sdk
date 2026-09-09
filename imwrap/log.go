package imwrap

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// Package-level logging. The wrapper and host programs share one
// logger so log output (format, level, destination) is managed in a
// single place. The default logger writes levelled text records to
// stderr.
var (
	logMu  sync.RWMutex
	logDef = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
)

// Logger returns the package logger. Host programs should use it for
// their own records so everything flows through the same handler.
func Logger() *slog.Logger {
	logMu.RLock()
	defer logMu.RUnlock()
	return logDef
}

// SetLogger replaces the package logger. Passing nil restores the
// default stderr text logger. Call before [New] so wrappers pick the
// replacement up (Config.Logger overrides per wrapper).
func SetLogger(l *slog.Logger) {
	logMu.Lock()
	defer logMu.Unlock()
	if l == nil {
		logDef = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
		return
	}
	logDef = l
}

// SetLogLevel adjusts the package logger's level by rebuilding the
// default text handler on stderr. It does not modify a logger
// installed via SetLogger.
func SetLogLevel(level slog.Level) {
	logMu.Lock()
	defer logMu.Unlock()
	logDef = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	}))
}

// SetLogFile routes the package logger to an append-only file (also
// mirrored to stderr) at the given level. It lets FileConfig's
// log_file option take over the whole program's log destination;
// callers wanting only a file logger can use NewFileLogger.
func SetLogFile(path string, level slog.Level) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", path, err)
	}
	multi := io.MultiWriter(os.Stderr, f)
	logMu.Lock()
	logDef = slog.New(slog.NewTextHandler(multi, &slog.HandlerOptions{Level: level}))
	logMu.Unlock()
	return nil
}

// NewFileLogger builds an independent logger writing to an
// append-only file; use it for host-specific logs that should not
// share the package logger.
func NewFileLogger(path string, level slog.Level) (*slog.Logger, *os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open log file %s: %w", path, err)
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})), f, nil
}

// Debug logs at debug level through the package logger. Hosts use
// these helpers (or Logger()) so every record flows through one
// handler.
func Debug(msg string, args ...any) { Logger().Debug(msg, args...) }

// Info logs at info level through the package logger.
func Info(msg string, args ...any) { Logger().Info(msg, args...) }

// Warn logs at warn level through the package logger.
func Warn(msg string, args ...any) { Logger().Warn(msg, args...) }

// Error logs at error level through the package logger.
func Error(msg string, args ...any) { Logger().Error(msg, args...) }
