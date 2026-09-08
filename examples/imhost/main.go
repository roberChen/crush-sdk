// Command imhost is a skeleton host program for the imwrap package:
// it bridges a hypothetical IM software whose client is a CLI (send
// text, send file, read history) to a Crush server. Replace the
// cliAdapter method bodies with the real CLI invocations and the
// polling loop with the IM's webhook or long-poll mechanism.
//
// The host only provides the IM adapter (and optional custom
// commands); session management, permissions, reports, question
// flows, model switching, and self-output filtering are handled by
// the wrapper.
//
// Usage: go run ./examples/imhost [-config imwrap.json] [-path /project/dir]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/roberChen/crush-sdk"
	"github.com/roberChen/crush-sdk/imwrap"
)

// cliAdapter implements imwrap.IMAdapter (and imwrap.HistoryFetcher)
// on top of three imaginary CLI commands:
//
//	im-cli send-text  <chat> <text>
//	im-cli send-file  <chat> <path>
//	im-cli history    <chat> <limit>
type cliAdapter struct{}

func (cliAdapter) SendText(ctx context.Context, chatID, text string) error {
	return runCLI(ctx, "send-text", chatID, text)
}

func (cliAdapter) SendFile(ctx context.Context, chatID, filename string, content []byte) error {
	path, err := writeTemp(filename, content)
	if err != nil {
		return err
	}
	return runCLI(ctx, "send-file", chatID, path)
}

func (cliAdapter) FetchHistory(ctx context.Context, chatID string, limit int) ([]imwrap.IMMessage, error) {
	out, err := outputCLI(ctx, "history", chatID, fmt.Sprint(limit))
	if err != nil {
		return nil, err
	}
	var msgs []imwrap.IMMessage
	for line := range strings.SplitSeq(out, "\n") {
		// Lines look like "id|sender:text".
		head, text, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		id, sender, _ := strings.Cut(head, "|")
		msgs = append(msgs, imwrap.IMMessage{ChatID: chatID, ID: id, Sender: sender, Text: text})
	}
	return msgs, nil
}

// runCLI/outputCLI/writeTemp are small helpers; swap them for the
// real process invocation details of your IM software.
func runCLI(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "im-cli", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("im-cli %v: %w: %s", args, err, out)
	}
	return nil
}

func outputCLI(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "im-cli", args...).Output()
	return string(out), err
}

func writeTemp(filename string, content []byte) (string, error) {
	dir, err := os.MkdirTemp("", "imwrap")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	return path, os.WriteFile(path, content, 0o600)
}

func main() {
	configPath := flag.String("config", "", "imwrap JSON config file (optional)")
	path := flag.String("path", ".", "workspace filesystem path")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Framework-owned logging: configure once here and use
	// imwrap.Logger() everywhere else in the host program.
	imwrap.SetLogLevel(slog.LevelInfo)
	hostLog := imwrap.Logger()

	// Options come from the config file when present; Client and
	// Adapter are always provided by the host program.
	cfg := imwrap.Config{}
	if *configPath != "" {
		fileCfg, err := imwrap.LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		fileCfg.Apply(&cfg)
		hostLog.Info("loaded config", "path", *configPath)
	}
	cfg.Adapter = cliAdapter{}
	if cfg.Client == nil {
		c, err := client.DefaultClient(*path)
		if err != nil {
			log.Fatalf("dial: %v", err)
		}
		cfg.Client = c
	}

	w, err := imwrap.New(cfg)
	if err != nil {
		log.Fatalf("wrapper: %v", err)
	}
	if err := w.Start(ctx); err != nil {
		log.Fatalf("start: %v", err)
	}
	defer w.Stop()

	// A custom command demonstrating the registration API.
	if err := w.RegisterCommand("ping", "reply pong", func(ctx context.Context, cc imwrap.CommandContext) error {
		return cc.Reply(ctx, "pong "+cc.ArgText)
	}); err != nil {
		log.Fatalf("register: %v", err)
	}

	// Poll conversation history and feed new messages to the
	// wrapper. The wrapper itself filters the bot's own output
	// (by message ID, sender account, or content echo), so this
	// loop can feed everything it sees. Replace with a webhook
	// push if the IM supports one.
	history, ok := w.Adapter().(imwrap.HistoryFetcher)
	if !ok {
		log.Fatal("adapter does not support history")
	}
	seen := make(map[string]bool)
	for {
		msgs, err := history.FetchHistory(ctx, "default-chat", 20)
		if err != nil {
			hostLog.Error("history failed", "error", err)
		}
		for _, m := range msgs {
			if m.ID != "" {
				if seen[m.ID] {
					continue
				}
				seen[m.ID] = true
			}
			if err := w.HandleMessage(ctx, m); err != nil {
				hostLog.Error("handle failed", "error", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
}
