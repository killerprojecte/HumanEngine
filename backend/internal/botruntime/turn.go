package botruntime

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/promptcache"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

// turnFrame contains only the current turn's handoff between native phases.
// Stages live through the complete pipeline, including later guarded effects.
type turnFrame struct {
	proceed               bool
	stages                []*stage
	extensions            []*Extension
	allTools              []*Object
	extensionTools        []*Object
	authority             any
	admitted              map[string]bool
	window                promptcache.Options
	participationKey      string
	recent                []*Object
	manualContext         string
	projection            *replyplan.ContextProjection
	imageContext          []any
	learned               []any
	extensionDependencies []string
	decision              *Object
	targets               []string
	replyerRecent         []*Object
	plannerKey            string
	grounding             *Grounding
	plan                  *Object
	deliveries            []*Object
	packedText            string
}

func (r *Runtime) execute(t *turn) {
	defer t.drainDone()
	var err error
	issues := 0
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("runtime_turn_panic: %v", cause)
		}
		if err != nil {
			t.result.Err = err
			t.result.Status = "failed"
			t.result.Reason = "turn_failed"
			if t.context.root.Aborted() {
				t.result.Status = "cancelled"
				t.result.Reason = "context_cancelled"
			}
		}
		r.finish(t, issues)
	}()
	err = r.pipeline(t, &issues)
}
func (r *Runtime) pipeline(t *turn, issues *int) error {
	f := new(turnFrame)
	defer func() {
		for i := len(f.stages) - 1; i >= 0; i-- {
			f.stages[i].close()
		}
	}()
	for _, phase := range []func(*turn, *turnFrame, *int) error{r.prepareTurn, r.participateTurn, r.planTurn} {
		f.proceed = false
		if err := phase(t, f, issues); err != nil || !f.proceed {
			return err
		}
	}
	return r.replyTurn(t, f, issues)
}
