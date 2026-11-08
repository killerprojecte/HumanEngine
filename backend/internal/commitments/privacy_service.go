package commitments

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Context struct {
	SceneID    string
	Simulation bool
}
type Reference = assertionauthority.Reference
type Assertion = assertionauthority.Result
type Transition struct{ From, To Reference }
type Candidate struct {
	Ref  Reference
	View *Assertion
}
type Surviving func(Context, *Object) (*Candidate, error)

// Owners is mandatory. The real workspace owner must consume these same-root
// persisted final/prior snapshots. A missing owner is not a production no-op.
type Owners struct {
	AfterWrite func(*Store, *Object, []any, []any) error
}
type PrivacyService struct {
	Store  *Store
	owners Owners
}

func NewPrivacyService(store *Store, owners Owners) (*PrivacyService, error) {
	if store == nil || owners.AfterWrite == nil {
		return nil, errors.New("commitment_owner_required")
	}
	return &PrivacyService{store, owners}, nil
}
func (s *PrivacyService) WithSession(session *recovery.WriteSession) *PrivacyService {
	n := *s
	n.Store = s.Store.WithSession(session)
	return &n
}
func reference(v any) Reference {
	return Reference{ID: str(get(v, "id")), Version: num(get(v, "version")), DeletionEpoch: num(get(v, "deletionEpoch"))}
}
func referenceValue(r Reference) *Object {
	return obj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
}
func bound(e *Object) any {
	if has(e, "qualificationRef") {
		return get(e, "qualificationRef")
	}
	return get(e, "backing")
}
func eligible(ctx Context, view *Assertion) bool {
	return view != nil && get(get(view.Record, "scope"), "kind") == "scene" && get(get(view.Record, "scope"), "sceneId") == ctx.SceneID && (get(view.Record, "status") == "active" || get(view.Record, "status") == "disputed") && get(view.Record, "lineageStatus") != "quarantined" && len(view.Proofs) > 0
}
func markerHead(head *Object) recovery.CommitmentMarkerHead {
	p := func(k string) *float64 {
		if get(head, k) == nil {
			return nil
		}
		n := num(get(head, k))
		return &n
	}
	h := recovery.CommitmentMarkerHead{ID: str(get(head, "id")), SceneID: str(get(head, "sceneId")), LatestVersion: num(get(head, "latestVersion")), ActiveVersion: p("activeVersion"), EverActivatedVersion: p("everActivatedVersion"), DeletionEpoch: num(get(head, "deletionEpoch")), MutationVersion: num(get(head, "mutationVersion")), Deleted: get(head, "deleted") == true}
	if term := get(head, "everTerminal"); term != nil {
		h.EverTerminal = &recovery.CommitmentTerminal{Version: num(get(term, "version")), State: str(get(term, "state"))}
	}
	return h
}
func (s *PrivacyService) record(kind string, head *Object, hash string, temporal bool, marker *Object) error {
	var emit func(recovery.Revocation) error
	if s.Store.maintenance != nil {
		scope := s.Store.maintenance
		if e := scope.requireReaders(temporal); e != nil {
			return e
		}
		emit = func(event recovery.Revocation) error {
			if e := scope.tx.RecordReplayEffect(scope.ctx, event); e != nil {
				// Preserve capability/cancellation errors. Only the existing typed
				// codec's contract rejection maps to the source domain error.
				switch e.Error() {
				case "recovery_unknown_revocation", "recovery_write_string_unsupported":
					return errors.New("recovery_commitment_event_invalid")
				}
				return e
			}
			return nil
		}
	} else {
		session := s.Store.session
		if e := session.RequireReader(3); e != nil {
			return e
		}
		if temporal {
			if e := session.RequireReader(10); e != nil {
				return e
			}
		}
		emit = func(event recovery.Revocation) error { _, e := session.AppendIntent(event); return e }
	}
	if get(head, "deleted") == true || kind == "delete" {
		return emit(recovery.CommitmentDelete{SceneID: str(get(head, "sceneId")), ID: str(get(head, "id")), DeletionEpoch: num(get(head, "deletionEpoch"))})
	}
	event := recovery.CommitmentHead{SchemaVersion: 1, SceneID: str(get(head, "sceneId")), ID: str(get(head, "id")), DeletionEpoch: num(get(head, "deletionEpoch")), TermsVersion: num(get(head, "latestVersion")), MutationVersion: num(get(head, "mutationVersion")), QualificationHash: hash}
	if temporal {
		event.SchemaVersion = 2
		one := float64(1)
		event.TemporalVersion = &one
	}
	if marker != nil {
		event.PrivacyMarker = &recovery.CommitmentPrivacyMarker{RecordedAt: str(get(marker, "recordedAt")), Head: markerHead(object(get(marker, "head"))), Reason: str(get(marker, "reason"))}
	}
	return emit(event)
}

// write prepares and qualifies the complete timeline before the durable intent.
// It never projects new declaration states during privacy maintenance.
func (s *PrivacyService) write(kind string, head *Object, revisions []any, prepared *Object) error {
	if e := s.Store.ready(); e != nil {
		return e
	}
	id := str(get(head, "id"))
	prior, e := s.Store.reader().Revisions(id)
	if e != nil {
		return e
	}
	old, e := s.Store.reader().Timeline(id)
	if e != nil {
		return e
	}
	timeline := prepared
	if timeline == nil {
		timeline = old
	}
	var marker *Object
	if get(head, "deleted") == true {
		timeline = nil
	} else if s.Store.maintenance == nil {
		row := obj("schemaVersion", float64(1), "id", get(head, "id"), "sceneId", get(head, "sceneId"), "mutationVersion", get(head, "mutationVersion"), "recordedAt", s.Store.now(), "reason", kind, "head", clone(head), "states", nil)
		marker, e = NormalizeMarker(obj("recordedAt", get(row, "recordedAt"), "head", get(row, "head"), "reason", kind))
		if e != nil {
			return e
		}
		timeline, e = AppendTimeline(timeline, row, s.Store.options.DateParser)
		if e != nil {
			return e
		}
	}
	if timeline != nil {
		timeline, e = NormalizeTimeline(timeline)
		if e != nil {
			return e
		}
	}
	hash, e := TemporalQualification(head, revisions, timeline, s.Store.options.Compare)
	if e != nil {
		return e
	}
	if e = s.record(kind, head, hash, timeline != nil || old != nil, marker); e != nil {
		return e
	}
	for _, previous := range prior {
		version := num(get(get(previous, "ref"), "version"))
		found := false
		for _, next := range revisions {
			if num(get(get(next, "ref"), "version")) == version {
				found = true
				break
			}
		}
		if !found {
			if e = s.Store.RemoveRevision(id, version); e != nil {
				return e
			}
		}
	}
	for _, next := range revisions {
		if e = s.Store.Put(head, object(next)); e != nil {
			return e
		}
	}
	if e = s.Store.Put(head, nil); e != nil {
		return e
	}
	if e = s.Store.ReplaceTimeline(id, timeline); e != nil {
		return e
	}
	e = s.owners.AfterWrite(s.Store, clone(head).(*Object), clone(revisions).([]any), clone(prior).([]any))
	return errors.Join(e, s.Store.requireTransaction())
}
func retainsTerms(revision *Object) bool {
	proposal := false
	for _, e := range arr(get(revision, "evidence")) {
		proposal = proposal || get(e, "kind") == "proposal"
	}
	if !proposal {
		return false
	}
	for _, g := range arr(get(revision, "requiredProposalGroups")) {
		if len(arr(g)) == 0 {
			return false
		}
	}
	return true
}
func (s *PrivacyService) transform(head *Object, transform evidenceTransform, kind string, retain bool) error {
	prior, e := s.Store.reader().Revisions(str(get(head, "id")))
	if e != nil {
		return e
	}
	var old, timeline *Object
	var next []any
	mapRevisions := func() error {
		next = []any{}
		for _, v := range prior {
			revision := object(v).Clone()
			evidence, e := mapEvidence(arr(get(revision, "evidence")), transform)
			if e != nil {
				return e
			}
			groups, e := transformGroups(get(revision, "requiredProposalGroups"), transform)
			if e != nil {
				return e
			}
			_ = revision.Set("evidence", evidence)
			_ = revision.Set("requiredProposalGroups", groups)
			if !retain || retainsTerms(revision) {
				next = append(next, revision)
			}
		}
		return nil
	}
	// Source requalify visits revisions before historical states; purge visits
	// historical states first. Owner predicates may fail, so this is observable.
	if kind == "requalify" {
		if e = mapRevisions(); e != nil {
			return e
		}
		old, e = s.Store.reader().Timeline(str(get(head, "id")))
		if e == nil {
			timeline, e = transformTemporal(old, transform)
		}
	} else {
		old, e = s.Store.reader().Timeline(str(get(head, "id")))
		if e == nil {
			timeline, e = transformTemporal(old, transform)
		}
		if e == nil {
			e = mapRevisions()
		}
	}
	if e != nil {
		return e
	}
	same, e := jsonEqual(prior, next)
	if e != nil {
		return e
	}
	if same {
		latest, e := s.Store.reader().Timeline(str(get(head, "id")))
		if e != nil {
			return e
		}
		timeSame, e := jsonEqual(timeline, latest)
		if e != nil {
			return e
		}
		if timeSame {
			return nil
		}
	}
	head = head.Clone()
	_ = head.Set("mutationVersion", num(get(head, "mutationVersion"))+1)
	return s.write(kind, head, next, timeline)
}
func (s *PrivacyService) purge(affected func(*Object) bool, assertionID *string) error {
	if e := s.Store.ready(); e != nil {
		return e
	}
	heads, e := s.Store.reader().AllHeads()
	if e != nil {
		return e
	}
	for _, v := range heads {
		transform := func(e *Object) (*Object, error) {
			proofs := []any{}
			if assertionID == nil || get(get(e, "backing"), "id") != *assertionID {
				for _, p := range arr(get(e, "proofs")) {
					if !affected(object(p)) {
						proofs = append(proofs, p)
					}
				}
			}
			if len(proofs) == 0 {
				return nil, nil
			}
			next := e.Clone()
			_ = next.Set("proofs", proofs)
			return next, nil
		}
		if e = s.transform(object(v), transform, "purge", true); e != nil {
			return e
		}
	}
	return nil
}
func (s *PrivacyService) PurgeSource(scene, message string) error {
	return s.purge(func(proof *Object) bool {
		for _, source := range arr(get(proof, "sources")) {
			if get(source, "sceneId") == scene && get(source, "messageId") == message {
				return true
			}
		}
		return false
	}, nil)
}
func (s *PrivacyService) PurgeAssertion(id string) error {
	return s.purge(func(proof *Object) bool {
		for _, parent := range arr(get(proof, "parents")) {
			if parent == id {
				return true
			}
		}
		return false
	}, &id)
}

// allowed is privacy availability, never current time/version applicability.
func (s *PrivacyService) PruneUnavailableProof(ctx Context, allowed func(*Object, *Object) (bool, error)) error {
	if e := s.Store.ready(); e != nil {
		return e
	}
	if ctx.Simulation {
		return nil
	}
	if allowed == nil {
		return errors.New("commitment_privacy_authority_required")
	}
	heads, e := s.Store.reader().AllHeads()
	if e != nil {
		return e
	}
	for _, h := range heads {
		if get(h, "sceneId") != ctx.SceneID || get(h, "deleted") == true {
			continue
		}
		transform := func(e *Object) (*Object, error) {
			proofs := []any{}
			for _, p := range arr(get(e, "proofs")) {
				ok, err := allowed(clone(e).(*Object), clone(p).(*Object))
				if err != nil {
					return nil, err
				}
				if ok {
					proofs = append(proofs, p)
				}
			}
			if len(proofs) == 0 {
				return nil, nil
			}
			next := e.Clone()
			_ = next.Set("proofs", proofs)
			return next, nil
		}
		if e = s.transform(object(h), transform, "purge", true); e != nil {
			return e
		}
	}
	return nil
}
func (s *PrivacyService) Requalify(ctx Context, transitions []Transition, surviving Surviving) error {
	if e := s.Store.ready(); e != nil {
		return e
	}
	if ctx.Simulation || surviving == nil {
		return nil
	}
	heads, e := s.Store.reader().AllHeads()
	if e != nil {
		return e
	}
	for _, h := range heads {
		if get(h, "sceneId") != ctx.SceneID || get(h, "deleted") == true {
			continue
		}
		transform := func(e *Object) (*Object, error) {
			var t *Transition
			for i := range transitions {
				if transitions[i].From == reference(bound(e)) {
					t = &transitions[i]
					break
				}
			}
			if t == nil {
				return e, nil
			}
			candidate, err := surviving(ctx, e)
			if err != nil {
				return nil, err
			}
			if candidate == nil || candidate.Ref.ID != str(get(get(e, "backing"), "id")) || candidate.Ref != t.To || !eligible(ctx, candidate.View) {
				return e, nil
			}
			fingerprint, err := BackingFingerprint(candidate.View.Record)
			if err != nil {
				return nil, err
			}
			if fingerprint != get(e, "backingFingerprint") {
				return e, nil
			}
			proofs := []any{}
			for _, p := range arr(get(e, "proofs")) {
				for _, v := range candidate.View.Proofs {
					if deepEqual(v, p) {
						proofs = append(proofs, p)
						break
					}
				}
			}
			if len(proofs) == 0 {
				return e, nil
			}
			ref, err := refValue(referenceValue(candidate.Ref))
			if err != nil {
				return nil, err
			}
			next := e.Clone()
			_ = next.Set("qualificationRef", ref)
			_ = next.Set("proofs", proofs)
			return next, nil
		}
		if e = s.transform(object(h), transform, "requalify", false); e != nil {
			return e
		}
	}
	return nil
}

// EraseRetained is a trusted privacy owner operation, not the interactive erase
// command's feature/exposure authorization. The caller must authorize that UI.
func (s *PrivacyService) EraseRetained(ctx Context, ref Reference) error {
	if e := s.Store.ready(); e != nil {
		return e
	}
	if ctx.Simulation {
		return errors.New("commitment_unavailable")
	}
	head, e := s.Store.reader().Head(ref.ID)
	if e != nil {
		return e
	}
	if head == nil || get(head, "sceneId") != ctx.SceneID || get(head, "deleted") == true || num(get(head, "deletionEpoch")) != ref.DeletionEpoch {
		return errors.New("commitment_unavailable")
	}
	head = head.Clone()
	_ = head.Set("deleted", true)
	_ = head.Set("deletionEpoch", num(get(head, "deletionEpoch"))+1)
	_ = head.Set("mutationVersion", num(get(head, "mutationVersion"))+1)
	if e = s.write("delete", head, []any{}, nil); e != nil {
		return e
	}
	return s.Store.Clear(ref.ID)
}
