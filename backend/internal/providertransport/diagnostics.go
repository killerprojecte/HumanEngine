package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

type DiagnosticSink interface {
	CaptureLazy(context.Context, func() (modeldiagnostics.Entry, error), []string) error
}

// Optional diagnostics cannot fail a completion, retry it, or lose continuation
// ownership. This runs before settlement inside the existing physical attempt
// barrier. Canceled late attempts do not restore diagnostics from dead work.
func (m *NativeModels) captureDiagnostic(ctx context.Context, role string, messages []map[string]any, c *Completion, custom bool, secrets []string) {
	defer func() { _ = recover() }()
	if m.Diagnostics == nil || ctx.Err() != nil {
		return
	}
	_ = m.Diagnostics.CaptureLazy(ctx, func() (modeldiagnostics.Entry, error) {
		ordered := make([]*jsjson.Object, len(messages))
		normalize := func(value any) (any, error) {
			raw, err := providerwire.EncodeJSON(value)
			if err != nil {
				return nil, err
			}
			return jsjson.ParseBytes(raw)
		}
		for i, message := range messages {
			value, err := normalize(message)
			if err != nil {
				return modeldiagnostics.Entry{}, err
			}
			var ok bool
			ordered[i], ok = value.(*jsjson.Object)
			if !ok {
				return modeldiagnostics.Entry{}, safe("invalid_diagnostic_message")
			}
		}
		output := new(jsjson.Object)
		_ = output.Set("text", c.Text)
		calls := c.ToolCalls
		if calls == nil {
			calls = []any{}
		}
		orderedCalls, err := normalize(calls)
		if err != nil {
			return modeldiagnostics.Entry{}, err
		}
		_ = output.Set("toolCalls", orderedCalls)
		if !custom {
			if c.Summary != nil {
				v, err := normalize(c.Summary)
				if err != nil {
					return modeldiagnostics.Entry{}, err
				}
				_ = output.Set("responseSummary", v)
			}
			if c.Usage != nil {
				v, err := normalize(c.Usage)
				if err != nil {
					return modeldiagnostics.Entry{}, err
				}
				_ = output.Set("usage", v)
			}
		}
		return modeldiagnostics.Entry{ModelID: c.ModelID, Role: role, Messages: ordered, Output: output}, nil
	}, secrets)
}
