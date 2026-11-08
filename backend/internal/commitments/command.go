package commitments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"regexp"
)

// CommandPolicy supplies invocation settings, not alternate evidence authority.
// The owner must read LearningEnabled from the same active assertion child.
type CommandPolicy struct {
	LearningEnabled bool
	ID              func() (string, error)
}
type CommandResult struct {
	Status string
	Reason string
	Item   *Object
}
type CommandService struct {
	store  *Store
	read   *ReadService
	owners Owners
}

// WithCommands borrows the current active child; it never opens a transaction.
// Its command handle expires with the actual assertion read scope.
func (s *Store) WithCommands(ctx context.Context, active *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, owners Owners, fn func(*CommandService) error) error {
	if s.maintenance != nil {
		return errors.New("commitment_live_session_required")
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if owners.AfterWrite == nil || fn == nil {
		return errors.New("commitment_owner_required")
	}
	return s.ReadCurrent(ctx, active, enabled, options, func(read *ReadService) error { return fn(&CommandService{s, read, owners}) })
}
func NormalizeCommand(v any) (*Object, error) {
	switch get(v, "kind") {
	case "propose":
		return shape(v, f("kind", enumeration("propose")), f("terms", termsValue))
	case "amend":
		return shape(v, f("kind", enumeration("amend")), f("target", refValue), f("terms", termsValue))
	default:
		return shape(v, f("kind", enumeration("confirm", "complete", "cancel")), f("target", refValue))
	}
}
func commandID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func selfPerson(v *Assertion) string {
	if get(v.Record, "epistemicKind") != "explicit" || get(v.Record, "status") != "active" {
		return ""
	}
	p := str(get(get(v.Record, "scope"), "personId"))
	if p == "" {
		return ""
	}
	for _, proof := range v.Proofs {
		w := get(proof, "subjectWitness")
		if get(w, "kind") != "self" || get(w, "personId") != p {
			continue
		}
		for _, s := range arr(get(proof, "sources")) {
			if get(s, "senderId") == p && get(s, "messageId") == get(w, "sourceMessageId") {
				return p
			}
		}
	}
	return ""
}

// people preserves the source's first available nonbot trusted evidence match
// and sender fallback. It rereads current authority, as memory.ts does.
func (s *CommandService) people(ctx Context, backing Reference, input *Object) ([]any, error) {
	v, e := s.read.current(ctx, backing)
	if e != nil || v == nil {
		return nil, e
	}
	out := []any{}
	add := func(p any) {
		for _, x := range out {
			if x == p {
				return
			}
		}
		out = append(out, p)
	}
	for _, p := range v.Proofs {
		for _, source := range arr(get(p, "sources")) {
			add(get(source, "senderId"))
			for _, ev := range arr(get(input, "sourceEvidence")) {
				if get(ev, "sceneId") == get(source, "sceneId") && get(ev, "messageId") == get(source, "messageId") && get(ev, "senderId") == get(source, "senderId") && get(ev, "available") == true && get(ev, "isBot") != true {
					for _, m := range arr(get(ev, "mentions")) {
						if get(m, "isBot") != true {
							add(get(m, "userId"))
						}
					}
					break
				}
			}
		}
	}
	return out, nil
}
func commandIncludes(a []any, v any) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

var commandClockPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT`)

func (s *CommandService) Admit(input *Object, rawBacking any, rawCommand any, policy CommandPolicy) (CommandResult, error) {
	rejected := func(reason string) (CommandResult, error) {
		return CommandResult{Status: "rejected", Reason: reason}, nil
	}
	fail := func(e error) (CommandResult, error) { return CommandResult{}, e }
	if e := s.read.check(); e != nil {
		return fail(e)
	}
	if e := s.store.requireTransaction(); e != nil {
		return fail(e)
	}
	ctx := Context{SceneID: str(get(input, "sceneId")), Simulation: get(input, "simulation") == true}
	if ctx.Simulation {
		return rejected("commitment_learning_disabled")
	}
	enabled, e := s.read.enabled(ctx)
	if e != nil {
		return fail(e)
	}
	if !enabled || !policy.LearningEnabled {
		return rejected("commitment_learning_disabled")
	}
	b, e := refValue(rawBacking)
	if e != nil {
		return fail(e)
	}
	command, e := NormalizeCommand(rawCommand)
	if e != nil {
		return fail(e)
	}
	backing := reference(b)
	view, e := s.read.current(ctx, backing)
	if e != nil {
		return fail(e)
	}
	if !eligible(ctx, view) || get(view.Record, "id") != backing.ID {
		return rejected("commitment_backing_unavailable")
	}
	kind := str(get(command, "kind"))
	target := reference(get(command, "target"))
	if kind != "propose" {
		var delivered any
		for _, r := range arr(get(input, "exposedRefs")) {
			if reference(r) == target {
				delivered = r
				break
			}
		}
		if get(input, "provenance") != "contextual" || delivered == nil {
			return rejected("commitment_not_exposed")
		}
		head, e := s.store.reader().Head(target.ID)
		if e != nil {
			return fail(e)
		}
		if head == nil || get(delivered, "mutationVersion") != get(head, "mutationVersion") {
			return rejected("commitment_snapshot_changed")
		}
	}
	key, e := digest([]any{ctx.SceneID, b, command})
	if e != nil {
		return fail(e)
	}
	previous, e := s.store.reader().Command(key)
	if e != nil {
		return fail(e)
	}
	if previous != nil {
		epoch := float64(0)
		if kind != "propose" {
			epoch = target.DeletionEpoch
		}
		item, e := s.read.Current(ctx, Reference{ID: str(get(previous, "id")), Version: num(get(previous, "version")), DeletionEpoch: epoch})
		if e != nil {
			return fail(e)
		}
		if item == nil {
			return rejected("commitment_unavailable")
		}
		return CommandResult{Status: "duplicate", Item: item}, nil
	}
	var head, revision *Object
	person := selfPerson(view)
	var fingerprint string
	evidence := func(kind string, self bool) *Object {
		proofs := view.Proofs
		if self {
			proofs = []any{}
			for _, p := range view.Proofs {
				w := get(p, "subjectWitness")
				if get(w, "kind") == "self" && get(w, "personId") == person {
					proofs = append(proofs, p)
				}
			}
		}
		ev := obj("backing", clone(b), "backingFingerprint", fingerprint, "proofs", clone(proofs))
		if self {
			_ = ev.Set("personId", person)
		}
		_ = ev.Set("kind", kind)
		return ev
	}
	if kind == "propose" || kind == "amend" {
		people, e := s.people(ctx, backing, input)
		if e != nil {
			return fail(e)
		}
		for _, p := range arr(get(get(command, "terms"), "participantIds")) {
			if !commandIncludes(people, p) {
				return rejected("commitment_participant_not_witnessed")
			}
		}
		if kind == "propose" {
			id := policy.ID
			if id == nil {
				id = commandID
			}
			value, e := id()
			if e != nil {
				return fail(e)
			}
			head = obj("id", value, "sceneId", ctx.SceneID, "latestVersion", float64(1), "activeVersion", nil, "everActivatedVersion", nil, "everTerminal", nil, "deletionEpoch", float64(0), "mutationVersion", float64(1), "deleted", false)
		} else {
			old, e := s.store.reader().Head(target.ID)
			if e != nil {
				return fail(e)
			}
			if old == nil || get(old, "deleted") == true || get(old, "sceneId") != ctx.SceneID || get(old, "deletionEpoch") != target.DeletionEpoch || get(old, "latestVersion") != target.Version {
				return rejected("commitment_version_conflict")
			}
			if num(get(old, "latestVersion")) >= 64 {
				return rejected("commitment_revision_limit")
			}
			item, e := s.read.Current(ctx, target)
			if e != nil {
				return fail(e)
			}
			if !has(item, "terms") {
				return rejected("commitment_unavailable")
			}
			head = old.Clone()
			_ = head.Set("latestVersion", num(get(old, "latestVersion"))+1)
			_ = head.Set("mutationVersion", num(get(old, "mutationVersion"))+1)
		}
		now := s.store.now()
		_, ok, e := s.store.options.DateParser.ParseMillis(now)
		if e != nil {
			return fail(e)
		}
		if !commandClockPattern.MatchString(now) || !ok {
			return fail(errors.New("commitment_clock_invalid"))
		}
		groups := []any{}
		if kind == "amend" {
			old, e := s.store.reader().Revision(target.ID, target.Version)
			if e != nil {
				return fail(e)
			}
			if old == nil {
				return fail(ErrCorrupt)
			}
			groups = clone(get(old, "requiredProposalGroups")).([]any)
			proposals := []any{}
			for _, ev := range arr(get(old, "evidence")) {
				if get(ev, "kind") == "proposal" {
					proposals = append(proposals, clone(ev))
				}
			}
			groups = append(groups, proposals)
		}
		fingerprint, e = BackingFingerprint(view.Record)
		if e != nil {
			return fail(e)
		}
		revision = obj("ref", obj("id", get(head, "id"), "version", get(head, "latestVersion"), "deletionEpoch", get(head, "deletionEpoch")), "sceneId", ctx.SceneID, "terms", clone(get(command, "terms")), "recordedAt", now, "requiredProposalGroups", groups, "evidence", []any{evidence("proposal", false)})
		count := 0
		for _, g := range groups {
			count += len(arr(g))
		}
		if len(groups) > 64 || count > 64 {
			return rejected("commitment_dependency_limit")
		}
		if person != "" && commandIncludes(arr(get(get(command, "terms"), "participantIds")), person) && canConfirm(revision) {
			_ = revision.Set("evidence", append(arr(get(revision, "evidence")), evidence("confirm", true)))
		}
	} else {
		old, e := s.store.reader().Head(target.ID)
		if e != nil {
			return fail(e)
		}
		prior, e := s.store.reader().Revision(target.ID, target.Version)
		if e != nil {
			return fail(e)
		}
		if old == nil || get(old, "deleted") == true || get(old, "sceneId") != ctx.SceneID || get(old, "deletionEpoch") != target.DeletionEpoch || prior == nil || get(prior, "sceneId") != ctx.SceneID {
			return rejected("commitment_unavailable")
		}
		if (kind == "confirm" && get(old, "latestVersion") != target.Version) || (kind != "confirm" && get(old, "activeVersion") != target.Version) {
			return rejected("commitment_version_conflict")
		}
		if person == "" || !commandIncludes(arr(get(get(prior, "terms"), "participantIds")), person) || !canConfirm(prior) {
			return rejected("commitment_self_declaration_required")
		}
		current, e := s.read.Current(ctx, target)
		if e != nil {
			return fail(e)
		}
		if !has(current, "terms") || (kind != "confirm" && get(current, "confirmation") != "confirmed") {
			return rejected("commitment_confirmation_incomplete")
		}
		if len(arr(get(prior, "evidence"))) >= 64 {
			return rejected("commitment_evidence_limit")
		}
		head = old.Clone()
		_ = head.Set("mutationVersion", num(get(old, "mutationVersion"))+1)
		revision = prior.Clone()
		fingerprint, e = BackingFingerprint(view.Record)
		if e != nil {
			return fail(e)
		}
		_ = revision.Set("evidence", append(arr(get(revision, "evidence")), evidence(kind, true)))
	}
	projection, e := ProjectCommitment(ctx, head, revision, s.read.current)
	if e != nil {
		return fail(e)
	}
	if get(projection, "confirmation") == "confirmed" {
		_ = head.Set("activeVersion", get(get(revision, "ref"), "version"))
		_ = head.Set("everActivatedVersion", get(get(revision, "ref"), "version"))
	}
	projection, e = ProjectCommitment(ctx, head, revision, s.read.current)
	if e != nil {
		return fail(e)
	}
	if get(projection, "lifecycle") == "done" || get(projection, "lifecycle") == "cancelled" {
		_ = head.Set("everTerminal", obj("version", get(get(revision, "ref"), "version"), "state", get(projection, "lifecycle")))
	}
	if e = s.write(kind, head, revision); e != nil {
		return fail(e)
	}
	if e = s.store.Remember(key, str(get(head, "id")), num(get(get(revision, "ref"), "version"))); e != nil {
		return fail(e)
	}
	item, e := ProjectCommitment(ctx, head, revision, s.read.current)
	if e != nil {
		return fail(e)
	}
	return CommandResult{Status: "saved", Item: item}, nil
}
