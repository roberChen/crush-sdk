// Package lsp contains the LSP state types shared between the Crush
// client and server. It mirrors the wire definitions of
// github.com/charmbracelet/crush/internal/lsp.
package lsp

// ServerState represents the state of an LSP server.
type ServerState int

const (
	StateUnstarted ServerState = iota
	StateStarting
	StateReady
	StateError
	StateStopped
	StateDisabled
)
