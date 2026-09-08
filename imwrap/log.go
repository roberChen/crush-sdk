package imwrap

import (
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
