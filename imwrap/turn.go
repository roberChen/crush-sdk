package imwrap

import (
	"github.com/roberChen/crush-sdk/proto"
)

// turnCollector accumulates the messages of one in-flight agent turn
// as they stream over SSE. Message events carry the full message on
// both "created" and "updated", so the collector keeps the latest
// version of each message keyed by ID and remembers first-seen order.
//
// A turnCollector is used only while holding the wrapper mutex (the
// event loop folds events and handlers snapshot under Wrapper.mu), so
// it needs no internal locking.
type turnCollector struct {
	order  []string
	msgs   map[string]*proto.Message
	prompt string
}

func newTurnCollector() *turnCollector {
	return &turnCollector{msgs: make(map[string]*proto.Message)}
}

// setPrompt records the user prompt that started the turn, used in
// the HTML report header.
func (t *turnCollector) setPrompt(prompt string) {
	t.prompt = prompt
}

// apply folds one message event into the collector.
func (t *turnCollector) apply(m proto.Message) {
	if m.ID == "" {
		return
	}
	if _, ok := t.msgs[m.ID]; !ok {
		t.order = append(t.order, m.ID)
	}
	msg := m
	t.msgs[m.ID] = &msg
}

// snapshot returns the collected messages in first-seen order. The
// copies share no state with the collector, so later events cannot
// mutate a returned slice.
func (t *turnCollector) snapshot() []proto.Message {
	out := make([]proto.Message, 0, len(t.order))
	for _, id := range t.order {
		if m, ok := t.msgs[id]; ok {
			out = append(out, *m)
		}
	}
	return out
}
