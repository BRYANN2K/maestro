package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/bryann2k/maestro/internal/runtimebridge"
)

// RuntimeProvider uses the provider adapters bundled with Maestro, including
// account authentication. This is model inference, never a vendor agent CLI.
type RuntimeProvider struct {
	ProviderName   string
	BaseURL        string
	Key            string
	Static         []Model
	SaveCredential func(context.Context, string, string) error
	once           sync.Once
	admission      chan struct{}
}

func (p *RuntimeProvider) Name() string { return p.ProviderName }
func (p *RuntimeProvider) Type() string { return "maestro" }
func (p *RuntimeProvider) Models() []Model {
	models := slices.Clone(p.Static)
	for i := range models {
		models[i].Efforts = slices.Clone(models[i].Efforts)
	}
	return models
}
func (p *RuntimeProvider) Discoverable() bool { return p.BaseURL != "" }
func (p *RuntimeProvider) Cost(req Request, u Usage) (Cost, error) {
	if err := validateUsage(u); err != nil {
		return Cost{}, err
	}
	for _, m := range p.Static {
		if m.ID == req.Model {
			cost := CostOf(m, u)
			return cost, validateCost(cost)
		}
	}
	return Cost{}, nil
}
func (p *RuntimeProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if _, err := runtimebridge.Path(); err != nil {
		return nil, err
	}
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		// Serialize refresh with inference for this credential so concurrent runs
		// cannot rotate the same refresh token independently.
		p.once.Do(func() { p.admission = make(chan struct{}, 1) })
		select {
		case p.admission <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-p.admission }()
		var credential struct {
			OAuth json.RawMessage `json:"maestro_oauth"`
		}
		_ = json.Unmarshal([]byte(p.Key), &credential)
		input := map[string]any{"version": 1, "operation": "provider", "provider": p.ProviderName, "baseURL": p.BaseURL, "key": p.Key, "request": req}
		if len(credential.OAuth) > 0 {
			input["oauth"] = credential.OAuth
			delete(input, "key")
		}
		err := runtimebridge.Run(ctx, input, func(ctx context.Context, m runtimebridge.Message) (any, error) {
			if m.Method == "credentials" {
				var result struct {
					Credentials json.RawMessage `json:"credentials"`
				}
				if err := json.Unmarshal(m.Params, &result); err != nil {
					return nil, err
				}
				encoded, _ := json.Marshal(map[string]any{"maestro_oauth": result.Credentials})
				if p.SaveCredential == nil {
					return nil, errors.New("credential refresh requires durable storage")
				}
				if err := p.SaveCredential(ctx, p.ProviderName, string(encoded)); err != nil {
					return nil, err
				}
				p.Key = string(encoded)
				return nil, nil
			}
			if m.Method != "event" {
				return nil, fmt.Errorf("unexpected provider method %q", m.Method)
			}
			var wire struct {
				Type    EventType
				Content json.RawMessage
			}
			if err := json.Unmarshal(m.Params, &wire); err != nil {
				return nil, err
			}
			var content any
			switch wire.Type {
			case EvTextDelta:
				content = &TextDelta{}
			case EvReasoningDelta:
				content = &ReasoningDelta{}
			case EvToolCall:
				content = &ToolCall{}
			case EvDone:
				content = &Done{}
			default:
				return nil, errors.New("invalid provider event")
			}
			if err := json.Unmarshal(wire.Content, content); err != nil {
				return nil, err
			}
			switch v := content.(type) {
			case *TextDelta:
				content = *v
			case *ReasoningDelta:
				content = *v
			case *ToolCall:
				content = *v
			case *Done:
				if v.Usage != nil {
					if err := validateUsage(*v.Usage); err != nil {
						return nil, err
					}
				}
				if v.Cost != nil {
					if err := validateCost(*v.Cost); err != nil {
						return nil, err
					}
				}
				content = *v
			}
			if !sendStreamEvent(ctx, ch, StreamEvent{Type: wire.Type, Content: content}) {
				return nil, ctx.Err()
			}
			return nil, nil
		})
		if err != nil {
			sendStreamEvent(ctx, ch, StreamEvent{Type: EvError, Content: StreamError{Message: err.Error()}})
		}
	}()
	return ch, nil
}

func RuntimeModels(ctx context.Context, provider string) ([]Model, error) {
	var models []Model
	err := runtimebridge.Run(ctx, map[string]any{"version": 1, "operation": "models", "provider": provider}, func(_ context.Context, m runtimebridge.Message) (any, error) {
		if m.Method != "models" {
			return nil, errors.New("invalid catalog response")
		}
		return nil, json.Unmarshal(m.Params, &models)
	})
	return models, err
}
