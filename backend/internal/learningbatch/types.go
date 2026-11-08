// Package learningbatch runs source-only extraction, repair, reconciliation and
// durable persistence. Admission/retry ownership remains with learningruntime.
package learningbatch

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"time"
)

type Object = jsjson.Object
type Totals struct{ CandidateCount, SavedCount, RejectedCount, IgnoredCount int }

// Applying is a permanent intent barrier, including ignored/rejected writes.
// WritePending stays true on an unknown or partial outcome, even with zero saves.
type Progress struct {
	Applying, WritePending bool
	Totals                 Totals
}
type Settings struct {
	Timezone                                           string
	GraphEnabled, CommitmentsEnabled, KnowledgeEnabled bool
	Proposal                                           memoryservice.ProposalOptions
}
type TargetScope struct {
	Targets  []*Object
	Coverage string
	Limits   *Object
}
type Reads interface {
	Settings(context.Context) (Settings, error)
	Enabled(context.Context, string) (bool, error)
	Recalled(context.Context, string, string) (bool, error)
	Search(context.Context, string, string) ([]*Object, error)
	Targets(context.Context, string, []*Object, []*Object, []any) (TargetScope, error)
	UnsupportedPronouns(context.Context, string, *Object, []any) (bool, error)
	CurrentSource(context.Context, *Object) (bool, error)
	AfterSaved(context.Context, string, *Object, *Object, []*Object) (*Object, error)
}
type Dependencies struct {
	Reads    Reads
	Memory   *memoryservice.Service
	Habits   *habitservice.Service
	Policy   *modelread.Policy
	Models   *providertransport.NativeModels
	Now      func() time.Time
	Disposed func() bool
	Log      func(string, string)
	Event    func(FlowEvent)
}
type Runner struct {
	d       Dependencies
	schemas *schemas
}

func New(d Dependencies) (*Runner, error) {
	if d.Reads == nil || d.Memory == nil || d.Habits == nil || d.Policy == nil || d.Models == nil {
		return nil, errors.New("learning_batch_dependencies_required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Disposed == nil {
		d.Disposed = func() bool { return false }
	}
	s, e := loadSchemas()
	if e != nil {
		return nil, e
	}
	return &Runner{d, s}, nil
}
func (r *Runner) log(level, event string) {
	if r.d.Log != nil {
		defer func() { _ = recover() }()
		r.d.Log(level, event)
	}
}
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func get(v any, k string) any {
	o, _ := v.(*Object)
	if o == nil {
		return nil
	}
	x, _ := o.Get(k)
	return x
}
func str(v any) string { s, _ := v.(string); return s }
func arr(v any) []any  { a, _ := v.([]any); return a }
func objects(v []*Object) []any {
	out := make([]any, len(v))
	for i, o := range v {
		out[i] = o
	}
	return out
}
func contains(a []any, v any) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func mode(s Settings) int {
	n := 0
	if s.GraphEnabled {
		n |= 1
	}
	if s.CommitmentsEnabled {
		n |= 2
	}
	if s.KnowledgeEnabled {
		n |= 4
	}
	return n
}
