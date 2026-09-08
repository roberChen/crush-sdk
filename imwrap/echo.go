package imwrap

import (
	"strings"
	"sync"
	"time"
)

// defaultEchoWindow is how long a sent message is remembered for
// self-output matching when the host does not supply message IDs.
const defaultEchoWindow = 10 * time.Minute

// maxEchoEntries caps the remembered self outputs per wrapper.
const maxEchoEntries = 512

// echoTracker recognizes the wrapper's own output when it comes back
// through the IM history feed. This matters when the user and the bot
// share one account (scenario 2): every SendText lands in the same
// conversation the host polls, and without filtering the wrapper would
// answer its own replies.
//
// Three mechanisms cooperate, in order of reliability:
//
//  1. Message IDs: the host marks the IDs of the bot's own messages
//     via [Wrapper.MarkSelfMessage] (best; works with [IMMessage.ID]).
//  2. Sender account: Config.SelfAccount names the bot account, so
//     messages from it are dropped outright (two-account scenario).
//  3. Content echo: everything the adapter sends is remembered
//     (chat-scoped, time-windowed); an incoming message with identical
//     text is treated as the bot's own output echoing back.
type echoTracker struct {
	mu      sync.Mutex
	ids     map[string]time.Time // "chatID|msgID" -> expiry
	texts   map[string][]echoEntry
	window  time.Duration
	nowFunc func() time.Time
}

type echoEntry struct {
	text   string
	expiry time.Time
}

func newEchoTracker(window time.Duration) *echoTracker {
	if window <= 0 {
		window = defaultEchoWindow
	}
	return &echoTracker{
		ids:     make(map[string]time.Time),
		texts:   make(map[string][]echoEntry),
		window:  window,
		nowFunc: time.Now,
	}
}

func echoKey(chatID, id string) string {
	return chatID + "|" + id
}

// normalize collapses whitespace so trivial IM-side reformatting does
// not defeat content matching.
func normalizeEcho(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// markID remembers a self message ID.
func (e *echoTracker) markID(chatID, id string) {
	if id == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gcLocked()
	e.ids[echoKey(chatID, id)] = e.nowFunc().Add(e.window)
}

// record remembers a self-sent text for content matching.
func (e *echoTracker) record(chatID, text string) {
	norm := normalizeEcho(text)
	if norm == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gcLocked()
	entries := e.texts[chatID]
	entries = append(entries, echoEntry{text: norm, expiry: e.nowFunc().Add(e.window)})
	if len(entries) > maxEchoEntries {
		entries = entries[len(entries)-maxEchoEntries:]
	}
	e.texts[chatID] = entries
}

// matchIDOnly checks remembered message IDs without content matching.
func (e *echoTracker) matchIDOnly(chatID, msgID string) bool {
	if msgID == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.ids[echoKey(chatID, msgID)]
	return ok
}

// match reports whether an incoming message is the bot's own output.
// msgID matching requires an exact remembered ID; text matching
// compares normalized content within the window. When msg.SentAt is
// non-zero, text matches additionally require the message to have been
// sent at or after the recording (minus a small clock-skew allowance).
func (e *echoTracker) match(chatID, msgID, text string, sentAt time.Time) bool {
	if msgID != "" {
		e.mu.Lock()
		_, ok := e.ids[echoKey(chatID, msgID)]
		e.mu.Unlock()
		if ok {
			return true
		}
	}
	norm := normalizeEcho(text)
	if norm == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.nowFunc()
	for _, entry := range e.texts[chatID] {
		if entry.text != norm {
			continue
		}
		if now.After(entry.expiry) {
			continue
		}
		if !sentAt.IsZero() && sentAt.Before(entry.expiry.Add(-e.window-2*time.Second)) {
			// Message predates the recording by more than the skew
			// allowance; it is a user message that happens to repeat
			// bot output.
			continue
		}
		return true
	}
	return false
}

// gcLocked drops expired entries; caller holds e.mu.
func (e *echoTracker) gcLocked() {
	now := e.nowFunc()
	for k, exp := range e.ids {
		if now.After(exp) {
			delete(e.ids, k)
		}
	}
	for chat, entries := range e.texts {
		kept := entries[:0]
		for _, entry := range entries {
			if now.After(entry.expiry) {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(e.texts, chat)
			continue
		}
		e.texts[chat] = kept
	}
}
