package agentcore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/runtimebridge"
)

func TestBundledHarnessToolGateAndHistory(t *testing.T) {
	if !runtimebridge.Available() {
		t.Skip("build bundled runtime and set MAESTRO_RUNTIME")
	}
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[deny], func(t *testing.T) {
			p := &fakeProvider{turns: []fakeTurn{{calls: []ToolCall{{ID: "t1", Name: "read", Args: `{"path":"a"}`}}}, {deltas: []string{"finished"}}}}
			executed := 0
			l := &Loop{Provider: p, Gate: GateFunc(func(context.Context, ToolCall, ToolSpec) error {
				if deny {
					return errors.New("no")
				}
				return nil
			}), Tools: map[string]Tool{"read": NewToolFunc(ToolSpec{Name: "read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}, func(context.Context, map[string]any) (string, error) { executed++; return "file", nil })}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := l.RunHarness(ctx, "read a"); err != nil {
				t.Fatal(err)
			}
			if l.LastAssistantText() != "finished" || len(l.History) != 4 {
				t.Fatalf("bad history: %+v", l.History)
			}
			want := 1
			if deny {
				want = 0
			}
			if executed != want {
				t.Fatalf("executed %d", executed)
			}
		})
	}
}
