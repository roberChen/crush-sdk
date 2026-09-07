package imwrap

import (
	"context"
	"time"
)

// IMAdapter bridges the wrapper to a concrete IM (instant messaging)
// client. The host program implements it on top of whatever the IM
// software offers, typically CLI commands for sending text, sending
// files, and reading conversation history.
//
// All methods may be called concurrently; implementations must be safe
// for concurrent use.
type IMAdapter interface {
	// SendText sends a plain-text message to the conversation
	// identified by chatID.
	SendText(ctx context.Context, chatID, text string) error

	// SendFile sends a file to the conversation identified by
	// chatID. content is the full file body; filename should carry
	// an extension the IM can render (the wrapper sends HTML
	// reports as filename ending in ".html").
	SendFile(ctx context.Context, chatID, filename string, content []byte) error
}

// HistoryFetcher is an optional interface an IMAdapter can implement
// when the IM software supports reading conversation history (for
// example a CLI command that prints recent messages). The wrapper
// itself does not require it; it exists so host programs and custom
// commands share one canonical shape for history access.
type HistoryFetcher interface {
	// FetchHistory returns up to limit most recent messages from
	// the conversation, oldest first. limit <= 0 means a
	// reasonable default chosen by the implementation.
	FetchHistory(ctx context.Context, chatID string, limit int) ([]IMMessage, error)
}

// IMMessage is one message observed in an IM conversation.
type IMMessage struct {
	// ChatID identifies the conversation the message belongs to.
	ChatID string
	// Sender identifies the author within the IM (user display
	// name, account, or ID).
	Sender string
	// Text is the plain-text body of the message.
	Text string
	// SentAt is when the message was sent; zero means unknown.
	SentAt time.Time
}
