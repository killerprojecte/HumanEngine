// Package memoryworkspace composes fresh current evidence and commitment
// protections. A metadata snapshot never grants access to record bodies.
package memoryworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type Object = jsjson.Object
type workspaceRead interface {
	AssertReady() error
	CurrentAssertion(commitments.Context, commitments.Reference) (*commitments.Assertion, error)
	SourceHead(string, string) (*Object, error)
	Heads(string) ([]any, error)
	Enabled(commitments.Context) (bool, error)
	Current(commitments.Context, commitments.Reference) (*Object, error)
}

// Resolver is callback-scoped and must not be used concurrently. It expires
// when the commitments.ReadCurrent callback that owns its ReadService returns.
type Resolver struct {
	read  workspaceRead
	dates *jsdate.Parser
}

func NewResolver(read *commitments.ReadService, dates *jsdate.Parser) (*Resolver, error) {
	if read == nil || dates == nil {
		return nil, errors.New("workspace_read_dependencies_required")
	}
	return &Resolver{read, dates}, nil
}
func get(v any, k string) any {
	o, _ := v.(*Object)
	if o == nil {
		return nil
	}
	x, _ := o.Get(k)
	return x
}
func object(v any) *Object { o, _ := v.(*Object); return o }
func str(v any) string     { s, _ := v.(string); return s }
func num(v any) float64    { n, _ := v.(float64); return n }
func arr(v any) []any      { a, _ := v.([]any); return a }
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func hash(v any) (string, error) {
	s, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:]), nil
}
func refValue(r workingset.Reference) *Object {
	return obj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
}
func ref(v any) commitments.Reference {
	return commitments.Reference{ID: str(get(v, "id")), Version: num(get(v, "version")), DeletionEpoch: num(get(v, "deletionEpoch"))}
}
func refKey(v any) string {
	s, _ := jsjson.Stringify([]any{get(v, "id"), get(v, "version"), get(v, "deletionEpoch")})
	return s
}
func clone(v any) any {
	switch x := v.(type) {
	case *Object:
		if x == nil {
			return nil
		}
		o := new(Object)
		for _, m := range x.Members() {
			_ = o.Set(m.Key, clone(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = clone(v)
		}
		return a
	}
	return v
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	}
	return true
}

// Snapshot uses actual current assertion authority, preserving repeated source
// occurrences and original JSON order in the qualification hash.
func (r *Resolver) snapshot(ctx commitments.Context, reference workingset.Reference) (*Object, error) {
	view, e := r.read.CurrentAssertion(ctx, ref(refValue(reference)))
	if e != nil || view == nil {
		return nil, e
	}
	record := view.Record
	scope := get(record, "scope")
	if truthy(get(record, "internalUse")) || get(scope, "kind") != "scene" || get(scope, "sceneId") != ctx.SceneID {
		return nil, nil
	}
	heads := []any{}
	for _, proof := range view.Proofs {
		for _, source := range arr(get(proof, "sources")) {
			scene, message := str(get(source, "sceneId")), str(get(source, "messageId"))
			head, e := r.read.SourceHead(scene, message)
			if e != nil {
				return nil, e
			}
			var value any
			if head != nil {
				value = head
			}
			heads = append(heads, obj("sceneId", scene, "messageId", message, "head", value))
		}
	}
	key, e := hash(obj("record", record, "proofs", view.Proofs, "heads", heads))
	if e != nil {
		return nil, e
	}
	return obj("ref", refValue(reference), "record", clone(record), "qualificationKey", key), nil
}
func (r *Resolver) planKey(view *Object) (string, error) {
	heads := []any{}
	evidence := append([]any{}, arr(get(view, "evidence"))...)
	for _, group := range arr(get(view, "requiredProposalGroups")) {
		evidence = append(evidence, arr(group)...)
	}
	for _, item := range evidence {
		for _, proof := range arr(get(item, "proofs")) {
			for _, source := range arr(get(proof, "sources")) {
				head, e := r.read.SourceHead(str(get(source, "sceneId")), str(get(source, "messageId")))
				if e != nil {
					return "", e
				}
				var value any
				if head != nil {
					value = head
				}
				heads = append(heads, value)
			}
		}
	}
	return hash(obj("view", view, "heads", heads))
}
func (r *Resolver) Resolve(ctx workingset.Context, refs []workingset.Reference) ([]workingset.Snapshot, error) {
	if e := r.read.AssertReady(); e != nil {
		return nil, e
	}
	out := []workingset.Snapshot{}
	if ctx.Simulation {
		return out, nil
	}
	// WorkspaceAuthority.resolveCurrentBatch performs its own readiness before
	// validating the combined source-defined input policy.
	if e := r.read.AssertReady(); e != nil {
		return nil, e
	}
	count := 0
	for _, ref := range refs {
		if ref.Kind == "assertion" || ref.Kind == "commitment" {
			count++
		}
	}
	if count > 40 {
		return nil, errors.New("workspace_candidate_limit")
	}
	c := commitments.Context{SceneID: ctx.SceneID}
	assertions := []*Object{}
	plans := []*Object{}
	for _, reference := range refs {
		if reference.Kind != "assertion" {
			continue
		}
		item, e := r.snapshot(c, reference)
		if e != nil {
			return nil, e
		}
		if item != nil {
			assertions = append(assertions, item)
		}
	}
	for _, reference := range refs {
		if reference.Kind != "commitment" {
			continue
		}
		view, e := r.read.Current(c, ref(refValue(reference)))
		if e != nil {
			return nil, e
		}
		if view != nil && reference.MutationVersion != nil && num(get(view, "mutationVersion")) == *reference.MutationVersion {
			plans = append(plans, object(clone(view)))
		}
	}
	heads, e := r.read.Heads(ctx.SceneID)
	if e != nil {
		return nil, e
	}
	enabled, e := r.read.Enabled(c)
	if e != nil {
		return nil, e
	}
	open := []*Object{}
	if enabled {
		for i, h := range heads {
			if i >= 200 {
				break
			}
			if get(h, "deleted") == true {
				continue
			}
			first := get(h, "activeVersion")
			if first == nil {
				first = get(h, "latestVersion")
			}
			versions := []any{first}
			if first != get(h, "latestVersion") {
				versions = append(versions, get(h, "latestVersion"))
			}
			for _, version := range versions {
				view, e := r.read.Current(c, commitments.Reference{ID: str(get(h, "id")), Version: num(version), DeletionEpoch: num(get(h, "deletionEpoch"))})
				if e != nil {
					return nil, e
				}
				if view != nil && (get(view, "lifecycle") == "pending" || get(view, "lifecycle") == "needs_review") {
					open = append(open, view)
				}
			}
		}
	}
	unknown := len(heads) > 200
	if !unknown {
		enabled, e = r.read.Enabled(c)
		if e != nil {
			return nil, e
		}
		if !enabled {
			again, e := r.read.Heads(ctx.SceneID)
			if e != nil {
				return nil, e
			}
			unknown = len(again) > 0
		}
	}
	protections := map[string]string{}
	dependencies := map[string][]any{}
	for _, plan := range open {
		protection := "open_commitment"
		if get(plan, "lifecycle") == "needs_review" {
			protection = "needs_review_commitment"
		}
		evidence := append([]any{}, arr(get(plan, "evidence"))...)
		for _, group := range arr(get(plan, "requiredProposalGroups")) {
			evidence = append(evidence, arr(group)...)
		}
		for _, item := range evidence {
			reference := get(item, "qualificationRef")
			if reference == nil {
				reference = get(item, "backing")
			}
			key := refKey(reference)
			qualified, e := r.planKey(plan)
			if e != nil {
				return nil, e
			}
			dependencies[key] = append(dependencies[key], obj("ref", get(plan, "ref"), "mutationVersion", get(plan, "mutationVersion"), "lifecycle", get(plan, "lifecycle"), "qualification", qualified))
			if protections[key] != "needs_review_commitment" {
				protections[key] = protection
			}
		}
	}
	for _, item := range assertions {
		reference := get(item, "ref")
		key := refKey(reference)
		deps := dependencies[key]
		if deps == nil {
			deps = []any{}
		}
		dependency, e := hash(obj("authority", get(item, "qualificationKey"), "plans", deps, "protectionUnknown", unknown))
		if e != nil {
			return nil, e
		}
		record := get(item, "record")
		value := workingset.Snapshot{Ref: workingset.Reference{Kind: "assertion", ID: str(get(reference, "id")), Version: num(get(reference, "version")), DeletionEpoch: num(get(reference, "deletionEpoch"))}, DependencyKey: dependency, SceneID: ctx.SceneID}
		if person := str(get(get(record, "scope"), "personId")); person != "" {
			value.PersonID = &person
		}
		if kind, ok := get(record, "epistemicKind").(string); ok {
			value.EpistemicKind = &kind
		}
		value.RecordedAt, e = r.parseTime(get(record, "learnedAt"))
		if e != nil {
			return nil, e
		}
		if protection, ok := protections[key]; ok {
			value.Protection = &protection
		} else if unknown {
			p := "unresolved_commitment_coverage"
			value.Protection = &p
		}
		out = append(out, value)
	}
	for _, view := range plans {
		reference := get(view, "ref")
		version := num(get(view, "mutationVersion"))
		dependency, e := r.planKey(view)
		if e != nil {
			return nil, e
		}
		value := workingset.Snapshot{Ref: workingset.Reference{Kind: "commitment", ID: str(get(reference, "id")), Version: num(get(reference, "version")), DeletionEpoch: num(get(reference, "deletionEpoch")), MutationVersion: &version}, DependencyKey: dependency, SceneID: ctx.SceneID, RecordedAt: nil}
		value.RecordedAt, e = r.parseTime(get(view, "recordedAt"))
		if e != nil {
			return nil, e
		}
		switch get(view, "lifecycle") {
		case "needs_review":
			p := "needs_review_commitment"
			value.Protection = &p
		case "pending":
			p := "open_commitment"
			value.Protection = &p
		}
		out = append(out, value)
	}
	return out, nil
}

func (r *Resolver) parseTime(value any) (*float64, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil, nil
	}
	millis, valid, e := r.dates.ParseMillis(text)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, nil
	}
	n := float64(millis)
	return &n, nil
}
