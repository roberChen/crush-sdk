// Command imhost is a skeleton host program for the imwrap package:
// it bridges a hypothetical IM software whose client is a CLI (send
// text, send file, read history) to a Crush server. Replace the
// cliAdapter method bodies with the real CLI invocations and the
// polling loop with the IM's webhook or long-poll mechanism.
//
// Usage: go run ./examples/imhost [-path /project/dir]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
		sender, text, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		msgs = append(msgs, imwrap.IMMessage{ChatID: chatID, Sender: sender, Text: text})
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
	path := flag.String("path", ".", "workspace filesystem path")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.DefaultClient(*path)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}

	w, err := imwrap.New(imwrap.Config{
		Client:  c,
		Adapter: cliAdapter{},
	})
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
	// wrapper. Replace with a webhook push if the IM supports one.
	seen := make(map[string]bool)
	history, ok := w.Adapter().(imwrap.HistoryFetcher)
	if !ok {
		log.Fatal("adapter does not support history")
	}
	for {
		msgs, err := history.FetchHistory(ctx, "default-chat", 20)
		if err != nil {
			log.Printf("history: %v", err)
		}
		for _, m := range msgs {
			key := m.Sender + "|" + m.Text
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := w.HandleMessage(ctx, m); err != nil {
				log.Printf("handle: %v", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
}
