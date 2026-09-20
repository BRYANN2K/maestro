package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bryann2k/maestro/internal/runtimebridge"
)

// RunHarness executes the forked OMP scheduler with Maestro-owned provider,
// budget, history and permission boundaries. No vendor coding CLI is involved.
func (l *Loop) RunHarness(ctx context.Context, prompt string) error {
	if l.Gate == nil {
		return errors.New("maestro harness requires an explicit tool gate")
	}
	if l.Stopper == nil {
		l.Stopper = NewStopper()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.Stopper.Reset(cancel)
	defer l.Stopper.Reset(nil)
	l.outputBytes = 0
	l.History = append(l.History, Message{Role: "user", Content: prompt})
	specs := make([]ToolSpec, 0, len(l.Tools))
	for _, t := range l.Tools {
		specs = append(specs, t.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	turns := 0
	limit := l.MaxTurns
	if limit <= 0 {
		limit = loopDefaultMaxTurns
	}
	maxTurn := l.MaxTurn
	if maxTurn <= 0 {
		maxTurn = loopDefaultMaxTurn
	}
	pending := map[string]ToolCall{}
	results := map[string]Message{}
	var turnCtx context.Context
	var endTurn context.CancelFunc
	defer func() {
		if endTurn != nil {
			endTurn()
		}
	}()
	err := runtimebridge.Run(ctx, map[string]any{"version": 1, "operation": "loop", "prompt": prompt, "tools": specs}, func(ctx context.Context, m runtimebridge.Message) (any, error) {
		switch m.Method {
		case "turn":
			if len(pending) > 0 {
				return nil, errors.New("runtime requested a turn before settling its tool batch")
			}
			if endTurn != nil {
				endTurn()
			}
			for {
				if turns >= limit {
					return nil, fmt.Errorf("loop exceeded %d turns", limit)
				}
				turns++
				turnCtx, endTurn = context.WithTimeout(ctx, maxTurn)
				assistant, calls, _, injected, err := l.oneTurn(turnCtx)
				if err != nil {
					return nil, err
				}
				if injected {
					endTurn()
					continue
				}
				l.History = append(l.History, assistant)
				for _, call := range calls {
					if call.ID == "" {
						return nil, errors.New("provider returned an empty tool call ID")
					}
					if _, exists := pending[call.ID]; exists {
						return nil, errors.New("provider returned duplicate tool call IDs")
					}
					pending[call.ID] = call
				}
				return map[string]any{"assistant": assistant}, nil
			}
		case "tool":
			var call ToolCall
			if err := json.Unmarshal(m.Params, &call); err != nil {
				return nil, err
			}
			original, ok := pending[call.ID]
			if !ok || original.Name != call.Name {
				return nil, errors.New("runtime requested an unadvertised tool call")
			}
			if _, ok := results[call.ID]; ok {
				return nil, errors.New("runtime attempted to replay a tool call")
			}
			// Execute the provider's exact arguments, not rewritten IPC arguments.
			if l.AntiLoop != nil && l.AntiLoop.Observe(original) {
				l.injectAntiLoop(original)
			}
			result, err := l.runTool(turnCtx, original)
			if err != nil {
				return nil, err
			}
			results[call.ID] = result
			return map[string]any{"output": result.Content}, nil
		case "tool_result":
			var value struct {
				ToolCallID string `json:"toolCallId"`
				ToolName   string `json:"toolName"`
				Content    []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(m.Params, &value); err != nil {
				return nil, err
			}
			original, ok := pending[value.ToolCallID]
			if !ok {
				return nil, errors.New("unexpected tool result")
			}
			result, executed := results[value.ToolCallID]
			if !executed {
				if !value.IsError {
					return nil, errors.New("runtime claimed success for an unexecuted tool")
				}
				var text strings.Builder
				for _, part := range value.Content {
					text.WriteString(part.Text)
				}
				if err := l.reserveOutput(text.Len()); err != nil {
					return nil, err
				}
				result = Message{Role: "tool", ToolCallID: original.ID, Name: original.Name, Content: "error: " + text.String()}
				l.emit(NewEvent(nil, l.systemRole(), EvToolResult, ToolResult{ID: original.ID, Name: original.Name, Err: text.String()}))
			}
			l.History = append(l.History, result)
			delete(pending, value.ToolCallID)
			delete(results, value.ToolCallID)
			return nil, nil
		default:
			return nil, fmt.Errorf("unknown runtime method %q", m.Method)
		}
	})
	if err != nil {
		return err
	}
	if len(pending) != 0 {
		return errors.New("runtime completed with unsettled tools")
	}
	return nil
}
