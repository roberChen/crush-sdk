package imwrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// ensureServer starts a `crush server` child process when the server
// is not already healthy, mirroring what `crush client` does. The
// child inherits the environment and is expected to bind the default
// per-user socket the client dials.
func (w *Wrapper) ensureServer(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := w.client.Health(probe)
	cancel()
	if err == nil {
		return nil
	}
	w.log.Info("imwrap: server not reachable, autostarting", "command", w.cfg.ServerCommand)

	cmd := exec.Command(w.cfg.ServerCommand, "server")
	cmd.Env = os.Environ()
	lw := logWriter{log: w.log, name: "server"}
	cmd.Stderr = lw
	cmd.Stdout = lw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s server: %w", w.cfg.ServerCommand, err)
	}
	w.mu.Lock()
	w.serverCmd = cmd
	w.mu.Unlock()

	deadline := time.Now().Add(w.cfg.ServerStartTimeout)
	for {
		probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := w.client.Health(probe)
		cancel()
		if err == nil {
			w.log.Info("imwrap: spawned server is healthy")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("spawned server did not become healthy within %s: %w", w.cfg.ServerStartTimeout, err)
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return errors.New("imwrap: cancelled while waiting for spawned server")
		}
	}
}

// logWriter pipes a child process's output into the wrapper logger.
type logWriter struct {
	log  *slog.Logger
	name string
}

func (lw logWriter) Write(p []byte) (int, error) {
	n := len(p)
	for _, line := range splitLogLines(string(p)) {
		if line == "" {
			continue
		}
		lw.log.Info("imwrap: " + lw.name + ": " + line)
	}
	return n, nil
}

func splitLogLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, trimCR(s[start:i]))
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, trimCR(s[start:]))
	}
	return out
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
