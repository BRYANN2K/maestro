package agent

import (
	"fmt"
)

// registered agents and their constructors.
var registry = map[string]func() Agent{
	"codex":    func() Agent { return NewCodexAgent() },
	"claude":   func() Agent { return NewClaudeAgent() },
	"cursor":   func() Agent { return NewCursorAgent() },
	"opencode": func() Agent { return NewOpenCodeAgent() },
	"grok":     func() Agent { return NewGrokAgent() },
	"kimi":     func() Agent { return NewKimiAgent() },
}

// Create builds the agent registered under name.
func Create(name string) (Agent, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown sub-agent %q (available: %v)", name, Names())
	}
	return f(), nil
}

// Names returns the registered agent names.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	return out
}
