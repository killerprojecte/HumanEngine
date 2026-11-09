package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func (r *Runtime) replyTurn(t *turn, f *turnFrame, issues *int) error {
	c, s, cfg := t.context, t.state, t.settings
	grounding := f.grounding
	plan := f.plan
	deliveries := f.deliveries
	packedText := f.packedText
	manualContext := f.manualContext
	projection := f.projection
	targets := f.targets
	replyerRecent := f.replyerRecent
	dialogueStage := r.newStage(t, "replyer")
	f.stages = append(f.stages, dialogueStage)
	user := obj("role", "user", "content", packedText)
	r.layout(user, "messages", cfg, nil)
	messages := []*Object{obj("role", "system", "content", cfg.Persona+"\n"+manualContext+"\n"+prompt("replyer")), user}
	delivery := toolNamed(deliveries, str(get(plan, "deliveryTool")))
	rejections, iterations := 0, 0
	for step := 0; step < replyplan.DialogueMaxRounds; step++ {
		if valid, err := r.eligible(c); err != nil {
			return err
		} else if !valid {
			break
		}
		iterations = step + 1
		result, err := r.completion(t, dialogueStage, "replyer", "replyer", messages, []*Object{delivery})
		if err != nil {
			return r.stageError(t, dialogueStage, "replyer", err)
		}
		if valid, err := r.eligible(c); err != nil {
			return err
		} else if !valid {
			break
		}
		messages = append(messages, result.Assistant)
		if len(result.ToolCalls) == 0 {
			t.result.Reason = "no_tool_calls"
			if result.Text != "" {
				t.result.Reason = "terminal_text_not_delivered"
			}
			break
		}
		for _, call := range result.ToolCalls {
			if valid, err := r.eligible(c); err != nil {
				return err
			} else if !valid {
				break
			}
			declared := call.Name == str(get(plan, "deliveryTool"))
			var output any
			var toolErr error
			if !declared {
				toolErr = code("dialogue_forbidden_tool")
			} else {
				var args any
				args, toolErr = projection.RestoreArguments(call.Arguments)
				if toolErr == nil {
					if call.Name == "messages.send" {
						receipts, e := r.send(t, dialogueStage, args)
						toolErr = e
						output = receiptsValue(receipts)
					} else {
						output, toolErr = r.executeExtensionTool(t, dialogueStage, call.Name, args, "send")
					}
				}
			}
			if toolErr == nil {
				_, toolErr = r.eligible(c)
			}
			if toolErr != nil {
				*issues++
				if len(t.toolIssues) < 16 {
					t.toolIssues = append(t.toolIssues, toolErr)
				}
				if !declared {
					return toolErr
				}
				if _, ok := toolErr.(*GroundingError); ok {
					rejections++
					output = obj("error", "grounding_rejected", "code", toolErr.Error(), "delivered", false)
				} else if contains([]string{"context_reference_unknown", "context_reference_cross_scene"}, toolErr.Error()) {
					output = obj("error", toolErr.Error(), "delivered", false)
				} else {
					output = obj("error", "Tool unavailable")
				}
			}
			if valid, err := r.eligible(c); err != nil {
				return err
			} else if !valid {
				break
			}
			grounding.ObserveTool(call.Name, output)
			text, err := projection.Stringify(output)
			if err != nil {
				return err
			}
			messages = append(messages, obj("role", "tool", "toolCallId", call.ID, "content", text))
			if r.hasOutcome(t) {
				break
			}
		}
		if r.hasOutcome(t) {
			break
		}
	}
	if !c.root.Aborted() {
		if err := context.Cause(dialogueStage); err != nil {
			return r.stageError(t, dialogueStage, "replyer", err)
		}
	}
	if iterations == replyplan.DialogueMaxRounds {
		valid, err := r.eligible(c)
		if err != nil {
			return err
		}
		if valid && t.result.Reason == "processing_finished" {
			t.result.Status = "partial"
			t.result.Reason = "tool_iteration_limit"
		}
	}
	fallbackTarget := ""
	if len(targets) == 1 && contains(ids(replyerRecent), targets[0]) {
		recalled, err := r.recalled(s, targets[0])
		if err != nil {
			return err
		}
		if !recalled {
			fallbackTarget = targets[0]
		}
	}
	fallbackEligible := false
	if rejections > 0 && !r.hasOutcome(t) {
		var err error
		fallbackEligible, err = r.eligible(c)
		if err != nil {
			return err
		}
	}
	if fallbackEligible && (!grounding.HasFeedbackReceipts() || fallbackTarget != "") {
		text := ""
		for i := len(replyerRecent) - 1; i >= 0; i-- {
			if get(replyerRecent[i], "senderId") != "bot" {
				text = GroundingText(rawParts(replyerRecent[i]))
				break
			}
		}
		segment := obj("parts", []any{obj("type", "text", "text", GroundingFallback(text))})
		if fallbackTarget != "" {
			_ = segment.Set("replyToId", fallbackTarget)
		}
		_, err := r.send(t, dialogueStage, obj("operationId", "grounding-safe-fallback", "segments", []any{segment}))
		if err != nil {
			return err
		}
	}
	return nil
}
