package tui

import (
	"bytes"
	"sync"
)

// CommandOutput captures backend prose while Bubble Tea owns the alternate
// screen. Draining it on the update loop prevents concurrent stdout writes
// from corrupting terminal frames.
type CommandOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func NewCommandOutput() *CommandOutput { return &CommandOutput{} }

func (o *CommandOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *CommandOutput) Drain() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	text := o.buf.String()
	o.buf.Reset()
	return text
}
