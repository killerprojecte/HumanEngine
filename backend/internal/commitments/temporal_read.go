package commitments

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

type Exposure struct {
	Reference
	MutationVersion float64
}
type TimelineQuery struct {
	AsOfRecorded *string
	Limit        *float64
}
type TimelinePorts struct {
	Revision   func(float64) (*Object, error)
	Historical func(Context, Reference) (*Assertion, error)
}

// CurrentEvidence intersects exact original proofs. Historical timeline reads
// use qualificationRef ?? backing through the historical authority, as source.
func CurrentEvidence(ctx Context, evidence *Object, current func(Context, Reference) (*Assertion, error)) (*Object, error) {
	if current == nil {
		return nil, errors.New("commitment_assertion_authority_required")
	}
	view, e := current(ctx, reference(bound(evidence)))
	if e != nil {
		return nil, e
	}
	if !eligible(ctx, view) || get(view.Record, "id") != get(get(evidence, "backing"), "id") {
		return nil, nil
	}
	fingerprint, e := BackingFingerprint(view.Record)
	if e != nil {
		return nil, e
	}
	if fingerprint != get(evidence, "backingFingerprint") {
		return nil, nil
	}
	proofs := []any{}
	for _, p := range arr(get(evidence, "proofs")) {
		for _, v := range view.Proofs {
			if get(v, "proofId") != get(p, "proofId") {
				continue
			}
			a, e := assertions.CanonicalProofKey(object(v))
			if e != nil {
				return nil, e
			}
			b, e := assertions.CanonicalProofKey(object(p))
			if e != nil {
				return nil, e
			}
			if a == b && deepEqual(v, p) {
				proofs = append(proofs, p)
				break
			}
		}
	}
	if len(proofs) == 0 {
		return nil, nil
	}
	next := clone(evidence).(*Object)
	_ = next.Set("proofs", clone(proofs))
	return next, nil
}

// ReadTimeline is the pure recorded-state read. The owner must first authorize
// the current scene/exposure/mutation anchor; this is not an interactive API.
func ReadTimeline(ctx Context, anchor Exposure, timeline *Object, input TimelineQuery, ports TimelinePorts) (*Object, error) {
	limit := float64(20)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if _, e := safe(1)(limit); e != nil || limit > 20 {
		return nil, errors.New("commitment_temporal_query_invalid")
	}
	if input.AsOfRecorded != nil {
		if _, e := instant(*input.AsOfRecorded); e != nil {
			return nil, errors.New("commitment_temporal_query_invalid")
		}
	}
	window := get(timeline, "window")
	firstTime, firstVersion := get(window, "firstRecordedAt"), get(window, "firstMutationVersion")
	pruned := get(window, "prunedThroughMutationVersion")
	if pruned == nil {
		pruned = float64(0)
	}
	states := []any{}
	reasons := []any{}
	result := obj("recordedStates", states, "coverage", "limited", "reasons", reasons, "interpretation", "previously_recorded_declaration_state_not_current_truth", "sourceBodyCoverage", "unavailable", "stateTime", "server_recorded_not_occurred", "window", obj("maxTransitions", float64(64), "firstRecordedAt", firstTime, "firstMutationVersion", firstVersion, "prunedThroughMutationVersion", pruned, "throughMutationVersion", anchor.MutationVersion))
	finish := func() *Object {
		_ = result.Set("recordedStates", states)
		_ = result.Set("reasons", reasons)
		return result
	}
	reason := func(value string) {
		for _, r := range reasons {
			if r == value {
				return
			}
		}
		reasons = append(reasons, value)
	}
	if timeline == nil {
		reason("legacy_history_unknown")
		return finish(), nil
	}
	if num(firstVersion) > 1 {
		reason("legacy_history_unknown")
	}
	if num(pruned) != 0 {
		reason("retention_incomplete")
	}
	if get(window, "clockNonmonotonic") == true {
		reason("recorded_clock_nonmonotonic")
		if input.AsOfRecorded != nil {
			return finish(), nil
		}
	}
	var cutoff int64
	if input.AsOfRecorded != nil {
		var valid bool
		var e error
		cutoff, valid, e = utcDate.ParseMillis(*input.AsOfRecorded)
		if e != nil || !valid {
			return nil, ErrSchema
		}
	}
	rows := []any{}
	for _, t := range arr(get(timeline, "transitions")) {
		if num(get(t, "mutationVersion")) > anchor.MutationVersion {
			continue
		}
		if input.AsOfRecorded != nil {
			ms, valid, e := utcDate.ParseMillis(str(get(t, "recordedAt")))
			if e != nil || !valid {
				return nil, ErrSchema
			}
			if ms > cutoff {
				continue
			}
		}
		rows = append(rows, t)
	}
	selected := rows
	if input.AsOfRecorded != nil {
		if len(rows) > 0 {
			selected = rows[len(rows)-1:]
		}
	} else if len(rows) > int(limit) {
		selected = rows[len(rows)-int(limit):]
	}
	if len(selected) == 0 {
		reason("no_recorded_state")
		return finish(), nil
	}
	if input.AsOfRecorded == nil && len(rows) > int(limit) {
		reason("result_limit")
	}
	for _, row := range selected {
		qualifiedStates := []any{}
		redacted := get(row, "states") == nil
		privacyRedacted := redacted
		for _, raw := range arr(get(row, "states")) {
			state := object(raw)
			if num(get(get(state, "ref"), "version")) > anchor.Version {
				redacted = true
				reason("anchor_version_bound")
				continue
			}
			if ports.Revision == nil {
				return nil, errors.New("commitment_revision_reader_required")
			}
			revision, e := ports.Revision(num(get(get(state, "ref"), "version")))
			if e != nil {
				return nil, e
			}
			if revision == nil || !has(state, "termsHash") {
				privacyRedacted = true
				redacted = true
				continue
			}
			hash, e := TemporalHash(get(revision, "terms"))
			if e != nil {
				return nil, e
			}
			if hash != get(state, "termsHash") {
				privacyRedacted = true
				redacted = true
				continue
			}
			historical := ports.Historical
			if historical == nil {
				historical = func(Context, Reference) (*Assertion, error) { return nil, nil }
			}
			qualified, e := QualifyRecordedState(state, func(evidence *Object) (*Object, error) { return CurrentEvidence(ctx, evidence, historical) })
			if e != nil {
				return nil, e
			}
			if qualified == nil {
				privacyRedacted = true
				redacted = true
				continue
			}
			view := qualified.Clone()
			view.Delete("termsHash")
			view.Delete("termsRecordedAt")
			_ = view.Set("terms", clone(get(revision, "terms")))
			if has(qualified, "termsRecordedAt") {
				_ = view.Set("recordedAt", get(qualified, "termsRecordedAt"))
			}
			_ = view.Set("evidence", get(qualified, "evidence"))
			_ = view.Set("requiredProposalGroups", get(qualified, "requiredProposalGroups"))
			qualifiedStates = append(qualifiedStates, view)
		}
		if privacyRedacted {
			reason("privacy_or_proof_unavailable")
		}
		status := "available"
		if redacted {
			status = "redacted"
		}
		states = append(states, obj("recordedAt", get(row, "recordedAt"), "mutationVersion", get(row, "mutationVersion"), "reason", get(row, "reason"), "activeVersion", get(get(row, "head"), "activeVersion"), "latestVersion", get(get(row, "head"), "latestVersion"), "status", status, "states", qualifiedStates))
	}
	return finish(), nil
}
