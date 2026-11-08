package learningbatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"strings"
)

type call struct {
	Name      string
	Arguments *Object
}
type batch struct {
	r          *Runner
	ctx        context.Context
	scene      string
	messages   []*Object
	settings   Settings
	projection *replyplan.ContextProjection
	ticket     *modelread.Ticket
	evidence   []any
}

func (b *batch) assert() error {
	if b.ticket != nil {
		// Permission revision is independent of cancellation. Source assert() is
		// synchronous: a cancelled provider context is not itself a revoked ticket.
		return b.r.d.Policy.Assert(context.WithoutCancel(b.ctx), b.ticket)
	}
	return nil
}
func (b *batch) prompt(key string) string {
	return b.r.schemas.prompts[key] + b.r.schemas.prompts[fmt.Sprintf("instructions%d", mode(b.settings))]
}
func (b *batch) now() string { return b.r.d.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func (b *batch) complete(system string, payload *Object, tools []map[string]any) ([]call, error) {
	if e := b.assert(); e != nil {
		return nil, e
	}
	text, e := b.projection.Stringify(payload)
	if e != nil {
		return nil, e
	}
	ctx := providertransport.WithModelReadGuard(b.ctx, func(ctx context.Context) error {
		if b.ticket != nil {
			return b.r.d.Policy.Assert(context.WithoutCancel(ctx), b.ticket)
		}
		return nil
	})
	response, e := b.r.d.Models.Complete(ctx, providertransport.CompletionRequest{Role: "learning", Messages: []*providertransport.Message{{Visible: map[string]any{"role": "system", "content": system}}, {Visible: map[string]any{"role": "user", "content": text}}}, Tools: tools})
	if e != nil {
		return nil, e
	}
	if response.Assistant != nil {
		defer response.Assistant.Release()
	}
	if e = b.assert(); e != nil {
		return nil, e
	}
	out := []call{}
	for _, raw := range response.ToolCalls {
		v, e := toObject(raw)
		if e != nil {
			return nil, e
		}
		a, _ := get(v, "arguments").(*Object)
		if a == nil {
			a = new(Object)
		} else {
			a = a.Clone()
		}
		name := str(get(v, "name"))
		if name == "learning.propose" || name == "learning.review_empty" {
			a.Delete("mergeIntoId")
			a.Delete("supersedesId")
		}
		restored, e := b.projection.RestoreArguments(a)
		if e != nil {
			if e.Error() != "context_reference_unknown" && e.Error() != "context_reference_cross_scene" {
				return nil, e
			}
			b.r.log("warn", e.Error())
			a = obj("invalidReference", true)
		} else {
			a, _ = restored.(*Object)
		}
		out = append(out, call{name, a})
	}
	return out, nil
}
func tool(name, description string, schema map[string]any) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}
func (r *Runner) Run(ctx context.Context, scene string, messages []*Object, progress *Progress) (bool, error) {
	if progress == nil {
		return false, errors.New("learning_progress_required")
	}
	s, e := r.d.Reads.Settings(ctx)
	if e != nil {
		return false, e
	}
	b := &batch{r: r, ctx: ctx, scene: scene, messages: messages, settings: s, projection: replyplan.NewContextProjection(scene)}
	allowed, e := r.d.Policy.PersonalizationAllowed(ctx, scene)
	if e != nil {
		return false, e
	}
	if allowed {
		b.ticket, e = r.d.Policy.Require(ctx, scene)
		if e != nil {
			return false, e
		}
	}
	b.evidence, e = sourceadmission.TrustedEvidence(messages, func(sc, id string) (bool, error) { return r.d.Reads.Recalled(ctx, sc, id) })
	if e != nil {
		return false, e
	}
	related := []*Object{}
	if b.ticket != nil {
		parts := []string{}
		for _, m := range messages {
			for _, p := range arr(get(m, "parts")) {
				if get(p, "type") == "text" {
					parts = append(parts, str(get(p, "text")))
				}
			}
		}
		related, e = r.d.Reads.Search(ctx, scene, sliceUTF16(strings.Join(parts, " "), 4000))
		if e != nil {
			return false, e
		}
	}
	if e = b.assert(); e != nil {
		return false, e
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	calls, extra, e := b.extract()
	if e != nil {
		return false, e
	}
	if e = b.repair(calls, extra); e != nil {
		return false, e
	}
	if e = b.assert(); e != nil {
		return false, e
	}
	if b.ticket != nil {
		if e = b.reconcile(calls, related); e != nil {
			return false, e
		}
	} else {
		r.log("info", "learning_reconciliation_read_disabled")
	}
	if e = b.assert(); e != nil {
		return false, e
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	persisted, err := b.persist(calls, progress)
	return persisted && ctx.Err() == nil, err
}
