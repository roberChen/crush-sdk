package imwrap

import (
	"context"
	"time"
)

// IMAdapter bridges the wrapper to a concrete IM (instant messaging)
// client. The host program implements it on top of whatever the IM
// software offers, typically CLI commands for sending text and
// files. Only SendText is mandatory; file sending is provided by
// implementing either [ContentFileSender] (send bytes) or
// [FilePathSender] (send a staged path, letting the wrapper own the
// file lifecycle).
//
// All methods may be called concurrently; implementations must be safe
// for concurrent use.
type IMAdapter interface {
	// SendText sends a plain-text message to the conversation
	// identified by chatID.
	SendText(ctx context.Context, chatID, text string) error
}

// ContentFileSender is an optional interface for adapters whose IM
// CLI accepts file contents directly.
type ContentFileSender interface {
	// SendFile sends a file to the conversation identified by
	// chatID. content is the full file body; filename should carry
	// an extension the IM can render (the wrapper sends HTML
	// reports as filename ending in ".html").
	SendFile(ctx context.Context, chatID, filename string, content []byte) error
}

// FilePathSender is an optional interface an IMAdapter implements
// when its IM CLI sends files by path rather than by content. When
// present, the wrapper takes over the file lifecycle: it writes the
// HTML report to Config.HTMLDir (a dedicated temp directory, never
// the program's working directory), passes the path to the adapter,
// and deletes the file after the send completes.
type FilePathSender interface {
	// SendFilePath sends the file at path to the conversation. The
	// file is owned by the wrapper and may be removed as soon as the
	// call returns.
	SendFilePath(ctx context.Context, chatID, filename, path string) error
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
	// ID is the IM-side message identifier, when the host knows it.
	// It enables exact self-output filtering: mark the bot's own
	// message IDs with [Wrapper.MarkSelfMessage].
	ID string
	// Sender identifies the author within the IM (user display
	// name, account, or ID). Compared against Config.SelfAccount.
	Sender string
	// Text is the plain-text body of the message.
	Text string
	// SentAt is when the message was sent; zero means unknown.
	SentAt time.Time
	// FromSelf lets the host declare the message as the bot's own
	// output outright (some IM SDKs flag direction), bypassing the
	// echo heuristics.
	FromSelf bool
}
