package replyplan

import (
	"context"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type ToolCall struct {
	ID        string
	Name      string
	Arguments any
}
type Completion struct {
	ToolCalls []ToolCall
	Assistant *Object
}

// Complete and Execute start work and return promptly, settling exactly once
// when that work finishes. This matches the source Promise initiation boundary
// without spawning unbounded goroutines for extensions that ignore cancellation.
// A late settlement after cancellation is accepted and discarded. The owning
// transport/extension remains responsible for stopping its actual operation.
type BuildInput struct {
	ValidationInput
	Messages         []*Object
	EvidenceMaxBytes *float64
	Projection       *ContextProjection
	ReadTools        []*Object
	Eligible         func() bool
	Complete         func(context.Context, []*Object, []*Object, func(Completion, error))
	Deliver          func(name string, value any, args any) (any, error)
	Execute          func(context.Context, string, any, func(any, error))
}
type BuildResult struct {
	Plan      *Object
	Evidence  []*Object
	Rounds    int
	ToolCalls int
}

var repairablePlanErrors = stringSet([]string{"planner_invalid_plan", "planner_invalid_target", "planner_unknown_memory_evidence", "planner_unknown_state_evidence", "planner_unknown_evidence", "planner_unknown_asset", "planner_incomplete_relation_evidence", "context_reference_unknown"})

// WithinSignal returns promptly if an extension ignores cancellation. Start is
// invoked before checking cancellation, as in withinSignal(workPromise, signal).
func WithinSignal[T any](ctx context.Context, start func(func(T, error))) (T, error) {
	return withinSignal(ctx, start, nil)
}

// attached is an internal test observation point after the source's initial
// throwIfAborted and Promise.race handler registration boundary.
func withinSignal[T any](ctx context.Context, start func(func(T, error)), attached func()) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	var mu sync.Mutex
	starting, claimed := true, false
	var early *result
	var zero T
	publish := func(r result) {
		if !claimed {
			claimed = true
			done <- r
		}
	}
	start(func(value T, err error) {
		mu.Lock()
		defer mu.Unlock()
		r := result{value, err}
		if starting {
			if early == nil {
				early = &r
			}
			return
		}
		if cause := context.Cause(ctx); cause != nil {
			publish(result{zero, cause})
		} else {
			publish(r)
		}
	})
	mu.Lock()
	starting = false
	if cause := context.Cause(ctx); cause != nil {
		publish(result{zero, cause})
	} else if early != nil {
		publish(*early)
	}
	mu.Unlock()
	stop := context.AfterFunc(ctx, func() { mu.Lock(); defer mu.Unlock(); publish(result{zero, context.Cause(ctx)}) })
	defer stop()
	if attached != nil {
		attached()
	}
	r := <-done
	return r.value, r.err
}
func (p *Policy) BuildReplyPlan(ctx context.Context, input BuildInput) (BuildResult, error) {
	finalTool := PlanTool(input.ReplySuggestionsEnabled)
	readTools := PlannerReadTools(input.ReadTools)
	messages := append([]*Object{}, input.Messages...)
	budget, err := NewEvidenceBudget(input.EvidenceMaxBytes)
	if err != nil {
		return BuildResult{}, err
	}
	evidence := append([]*Object{}, input.Evidence...)
	toolCalls := 0
	repairing := false
	var repairActions any = []any{}
	deliveredReceipts := []*Object{}
	check := func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if !input.Eligible() {
			return errCode("planner_context_ineligible")
		}
		return nil
	}
	restore := func(v any) (any, error) {
		if input.Projection != nil {
			return input.Projection.RestoreArguments(v)
		}
		return v, nil
	}
	serialize := stringify
	if input.Projection != nil {
		serialize = input.Projection.Stringify
	}
	for round := 0; round < PlannerMaxRounds; round++ {
		if err := check(); err != nil {
			return BuildResult{}, err
		}
		if err := budget.AssertWithin(messages); err != nil {
			return BuildResult{}, err
		}
		tools := []*Object{finalTool}
		if !repairing {
			tools = append(append([]*Object{}, readTools...), finalTool)
		}
		result, err := WithinSignal(ctx, func(settle func(Completion, error)) { input.Complete(ctx, messages, tools, settle) })
		if err != nil {
			return BuildResult{}, err
		}
		if err := check(); err != nil {
			return BuildResult{}, err
		}
		final := []ToolCall{}
		for _, call := range result.ToolCalls {
			if call.Name == "reply.plan" {
				final = append(final, call)
			}
		}
		if len(final) > 0 {
			if len(final) != 1 || len(result.ToolCalls) != 1 {
				return BuildResult{}, errCode("planner_ambiguous_plan")
			}
			restored, err := restore(final[0].Arguments)
			var plan *Object
			if err == nil {
				validation := input.ValidationInput
				validation.Evidence = evidence
				plan, err = p.ValidateReplyPlan(restored, validation)
			}
			if err == nil && repairing && !deepEqual(get(plan, "actions"), repairActions) {
				err = errCode("planner_forbidden_action")
			}
			if err == nil {
				return BuildResult{plan, evidence, round + 1, toolCalls}, nil
			}
			if repairing || round+1 >= PlannerMaxRounds || !repairablePlanErrors[err.Error()] {
				return BuildResult{}, err
			}
			if err := check(); err != nil {
				return BuildResult{}, err
			}
			repairActions = []any{}
			initial, restoreErr := restore(final[0].Arguments)
			if restoreErr == nil {
				parsed, parseErr := parsePlan(initial, input.ReplySuggestionsEnabled)
				if parseErr == nil {
					repairActions = clone(get(parsed, "actions"))
				}
			}
			hint := repairHint(err.Error(), deliveredReceipts)
			content, err := serialize(hint)
			if err != nil {
				return BuildResult{}, err
			}
			messages = append(messages, result.Assistant, obj(member("role", "tool"), member("toolCallId", final[0].ID), member("content", content)))
			if err := budget.AssertWithin(messages); err != nil {
				return BuildResult{}, err
			}
			repairing = true
			continue
		}
		if len(result.ToolCalls) == 0 {
			return BuildResult{}, errCode("planner_missing_plan")
		}
		if repairing {
			return BuildResult{}, errCode("planner_forbidden_tool")
		}
		if toolCalls+len(result.ToolCalls) > PlannerMaxToolCalls {
			return BuildResult{}, errCode("planner_tool_budget_exhausted")
		}
		messages = append(messages, result.Assistant)
		if err := budget.AssertWithin(messages); err != nil {
			return BuildResult{}, err
		}
		for _, call := range result.ToolCalls {
			if err := check(); err != nil {
				return BuildResult{}, err
			}
			if !toolExists(readTools, call.Name) {
				return BuildResult{}, errCode("planner_forbidden_tool")
			}
			toolCalls++
			args, err := restore(call.Arguments)
			if err != nil {
				return BuildResult{}, err
			}
			value, err := WithinSignal(ctx, func(settle func(any, error)) { input.Execute(ctx, call.Name, args, settle) })
			if err != nil {
				return BuildResult{}, err
			}
			if err := check(); err != nil {
				return BuildResult{}, err
			}
			delivered, err := budget.Receipt(messages, obj(member("id", "tool:"+integerString(toolCalls)), member("toolName", call.Name), member("value", value)), call.ID, serialize)
			if err != nil {
				return BuildResult{}, err
			}
			expected := clone(get(delivered.Receipt, "value"))
			accepted := get(delivered.Receipt, "value")
			if input.Deliver != nil {
				args, err := restore(call.Arguments)
				if err != nil {
					return BuildResult{}, err
				}
				accepted, err = input.Deliver(call.Name, get(delivered.Receipt, "value"), args)
				if err != nil {
					return BuildResult{}, err
				}
			}
			if !deepEqual(accepted, expected) || !deepEqual(get(delivered.Receipt, "value"), expected) {
				return BuildResult{}, errCode("planner_context_ineligible")
			}
			evidence = append(evidence, delivered.Receipt)
			deliveredReceipts = append(deliveredReceipts, delivered.Receipt)
			messages = append(messages, delivered.Message)
		}
	}
	return BuildResult{}, errCode("planner_round_budget_exhausted")
}
func repairHint(code string, receipts []*Object) *Object {
	available := []*Object{}
	for _, receipt := range receipts {
		if oneOf(get(receipt, "toolName"), "memory.search", "memory.person", "memory.relations") && isArray(get(receipt, "value")) {
			available = append(available, receipt)
		}
	}
	selected := available
	if len(selected) > 6 {
		selected = selected[:6]
	}
	options := []any{}
	for _, receipt := range selected {
		ids := []any{}
		for _, item := range array(get(receipt, "value")) {
			if object(item) != nil && isString(get(item, "id")) {
				s := str(get(item, "id"))
				n, err := jsjson.UTF16Len(s)
				if err == nil && n <= 200 {
					ids = append(ids, s)
				}
			}
		}
		limited := get(get(receipt, "coverage"), "status") == "limited" || len(ids) > 12
		status := "complete"
		if limited {
			status = "limited"
		}
		omitted := 0
		if len(ids) > 12 {
			omitted = len(ids) - 12
			ids = ids[:12]
		}
		option := obj(member("evidenceId", get(receipt, "id")), member("retrievalCoverage", RetrievalCoverage(str(get(receipt, "toolName")))), member("memoryIds", ids), member("coverage", status))
		if truthy(get(receipt, "coverage")) {
			set(option, "receiptCoverage", get(receipt, "coverage"))
		}
		set(option, "hintOmittedRecords", float64(omitted))
		options = append(options, option)
	}
	limited := len(available) > len(selected)
	for _, receipt := range receipts {
		limited = limited || get(get(receipt, "coverage"), "status") == "limited"
	}
	for _, receipt := range selected {
		limited = limited || get(get(receipt, "coverage"), "status") == "limited" || len(array(get(receipt, "value"))) > 12
	}
	status := "complete"
	if limited {
		status = "limited"
	}
	return obj(member("error", obj(member("code", code), member("retryable", true), member("remainingRepairs", float64(1)))), member("instruction", "Return only reply.plan. evidenceId is outer receipt.id; memoryIds must be inner record.id values from that same receipt.value. No reads or actions execute during repair. Preserve the initial valid actions array exactly; if the initial plan was malformed, actions must be empty. Reuse the existing plan contract; do not invent references."), member("memorySelectionOptions", options), member("coverage", status))
}
