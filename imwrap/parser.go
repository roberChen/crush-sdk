package imwrap

import (
	"strings"
)

// Kind classifies a parsed IM message.
type Kind int

const (
	// KindIgnore marks blank or malformed messages that trigger
	// nothing (for example a bare command prefix).
	KindIgnore Kind = iota
	// KindCommand marks a message that invokes a command.
	KindCommand
	// KindAgent marks a message that should be sent to the agent
	// as a prompt.
	KindAgent
)

// String returns a human-readable name for the kind.
func (k Kind) String() string {
	switch k {
	case KindCommand:
		return "command"
	case KindAgent:
		return "agent"
	default:
		return "ignore"
	}
}

// Parsed is the result of parsing one IM message body.
type Parsed struct {
	// Kind is the classification of the message.
	Kind Kind
	// Raw is the trimmed original text.
	Raw string
	// Name is the command name without the prefix, lowercased.
	// Only set for KindCommand.
	Name string
	// Args are the whitespace-separated arguments after the
	// command name. Only set for KindCommand.
	Args []string
	// ArgText is the raw remainder of the text after the command
	// name, with leading/trailing space removed. Only set for
	// KindCommand.
	ArgText string
}

// Parse classifies an IM message body. prefix is the command prefix
// (Config.CommandPrefix, default "/"). A message is a command when it
// starts with the prefix followed by a non-space command name;
// everything else (non-empty) is an agent prompt. Empty or
// whitespace-only text parses to KindIgnore, as does a lone prefix.
//
// The question-answer intercept happens before Parse in the wrapper's
// message routing, so free-text answers to agent questions are never
// mistaken for prompts.
func Parse(prefix, text string) Parsed {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return Parsed{Kind: KindIgnore}
	}
	if prefix != "" && strings.HasPrefix(raw, prefix) {
		rest := raw[len(prefix):]
		if rest == "" || strings.HasPrefix(rest, " ") {
			return Parsed{Kind: KindIgnore}
		}
		fields := strings.Fields(rest)
		name := strings.ToLower(fields[0])
		if name == "" {
			return Parsed{Kind: KindIgnore}
		}
		argText := strings.TrimSpace(rest[len(fields[0]):])
		return Parsed{
			Kind:    KindCommand,
			Raw:     raw,
			Name:    name,
			Args:    fields[1:],
			ArgText: argText,
		}
	}
	return Parsed{Kind: KindAgent, Raw: raw}
}
