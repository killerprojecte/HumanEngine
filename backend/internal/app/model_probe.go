package app

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

const probePNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAACXBIWXMAAAPoAAAD6AG1e1JrAAAADElEQVQImWP4//8/AAX+Av5Y8msOAAAAAElFTkSuQmCC"

type probeAdmission struct {
	db        *sql.DB
	authority *recovery.Authority
	budget    *providertransport.BudgetService
}

func (p probeAdmission) Current(ctx context.Context) (gate providertransport.BudgetGate, err error) {
	err = p.authority.Publish(ctx, p.db, func(conn *sql.Conn) ([]byte, error) {
		gate, err = p.budget.CurrentOnSnapshot(ctx, conn)
		return nil, err
	}, func([]byte) error { return nil })
	return
}

func (a *Application) modelProbe(owners Owners, client *providertransport.Client, sched *scheduler.Scheduler) httpapi.ModelProbe {
	return func(ctx context.Context, id, capability string) (httpapi.ModelProbeResult, error) {
		start := time.Now()
		result := func(ok bool, detail string) (httpapi.ModelProbeResult, error) {
			return httpapi.ModelProbeResult{OK: ok, Capability: capability, Detail: detail, DurationMS: time.Since(start).Milliseconds()}, nil
		}
		role := "replyer"
		if capability == "vision" || capability == "embedding" {
			role = capability
		}
		var input providertransport.EmbeddingModel
		var budgetMS float64
		var missing string
		router, release, err := a.models.AcquireOperationRouter(ctx, func(call context.Context) (*providertransport.NativeModels, error) {
			config, err := readConfiguration(call, owners.Main, owners.Authority)
			if err != nil {
				return nil, err
			}
			var selected, provider map[string]any
			for _, raw := range config.models["models"].([]any) {
				if model := raw.(map[string]any); model["id"] == id {
					selected = model
					break
				}
			}
			if selected == nil {
				missing = "model_not_found"
				return nil, modelservice.NewError(missing, nil, false)
			}
			for _, raw := range config.models["providers"].([]any) {
				if row := raw.(map[string]any); row["id"] == selected["providerId"] {
					provider = row
					break
				}
			}
			if provider == nil {
				missing = "provider_not_configured"
				return nil, modelservice.NewError(missing, nil, false)
			}
			input = providertransport.EmbeddingModel{ID: id, ProviderID: provider["id"].(string), APIModelName: selected["apiModelName"].(string)}
			if n, ok := selected["embeddingDimensions"].(float64); ok {
				v := int(n)
				input.Dimensions = &v
			}
			if n, ok := selected["embeddingBatchSize"].(float64); ok {
				v := int(n)
				input.BatchSize = &v
			}
			budgetMS, err = modelservice.ProviderOperationBudget(modelservice.Provider{RetryCount: numberOf(provider["retryCount"]), TimeoutMS: numberOf(provider["timeoutMs"])})
			if err != nil {
				return nil, err
			}
			selected = maps.Clone(selected)
			caps := []any{"text"}
			if capability != "text" {
				caps = append(caps, capability)
			}
			selected["capabilities"] = caps
			configured := map[string]any{
				"models": []any{selected}, "providers": []any{provider},
				"routes": []any{map[string]any{"role": role, "orderedModelIds": []any{id}, "timeoutMs": budgetMS}},
			}
			probeClient := *client
			probeClient.MaxOutputTokens = nil // The source probe has no static router cap; the budget gate still applies.
			probeClient.Budgets = probeAdmission{owners.Main, owners.Authority, owners.Budgets}
			probeClient.CheckUnavailableProtocolBudget = true
			next, err := modelConfiguration(configured, &probeClient, modelservice.Options{Scheduler: sched})
			if err != nil {
				return nil, err
			}
			next.Host, next.Diagnostics, next.Calls, next.Usage = a.models.Host, a.models.Diagnostics, a.models.Calls, a.models.Usage
			return next, nil
		})
		if err != nil {
			if missing != "" {
				return result(false, missing)
			}
			return httpapi.ModelProbeResult{}, err
		}
		defer release()
		bounded, stop := router.Policy.StartProbeDeadline(ctx, budgetMS)
		defer stop()
		failure := func(err error) (httpapi.ModelProbeResult, error) {
			var model *modelservice.ModelError
			if errors.As(err, &model) {
				if capability == "embedding" && model.Code == "invalid_embedding_vector" {
					return result(false, "embedding_validation_failed")
				}
				return result(false, model.Code)
			}
			return result(false, "probe_failed")
		}
		if capability == "embedding" {
			_, err := router.Embeddings(bounded, input, []string{"HumanEngine capability probe."})
			if err != nil {
				return failure(err)
			}
			return result(true, "embedding_vector_validated")
		}
		visible := map[string]any{"role": "user"}
		request := providertransport.CompletionRequest{Role: role, Messages: []*providertransport.Message{{Visible: visible}}, Tools: []map[string]any{}}
		switch capability {
		case "tools":
			visible["content"] = `Call probe.echo exactly once with value "humanengine-probe". Do not answer with text.`
			request.Tools = []map[string]any{{
				"name": "probe.echo", "description": "Return the supplied fixed probe value. This has no external side effects.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []any{"humanengine-probe"}}}, "required": []any{"value"}, "additionalProperties": false},
			}}
		case "vision":
			visible["content"] = "This is a protocol image-input acceptance test. Acknowledge this request briefly."
			visible["images"] = []any{probePNG}
		default:
			visible["content"] = "This is a text protocol test. Reply briefly with OK."
		}
		response, err := router.Complete(bounded, request)
		if err != nil {
			return failure(err)
		}
		defer response.Release()
		if capability == "tools" {
			if len(response.ToolCalls) == 1 {
				if call, ok := response.ToolCalls[0].(map[string]any); ok && call["name"] == "probe.echo" {
					if arguments, ok := call["arguments"].(*jsjson.Object); ok && arguments.Len() == 1 {
						if value, _ := arguments.Get("value"); value == "humanengine-probe" {
							return result(true, "tool_call_validated")
						}
					}
				}
			}
			return result(false, "tool_call_validation_failed")
		}
		if jsstring.Trim(response.Text) == "" || len(response.ToolCalls) != 0 {
			return result(false, "text_response_missing")
		}
		if capability == "vision" {
			return result(true, "vision_input_accepted_accuracy_unverified")
		}
		return result(true, "text_response_received")
	}
}
