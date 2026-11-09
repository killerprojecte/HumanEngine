package commitments

import "errors"

// History requires an actually exposed current ref supplied by the owner. Its
// views requalify original proof occurrences through historical assertion
// authority; they do not grant current commitment or learning authority.
func (s *ReadService) History(ctx Context, ref Reference, exposed []Exposure) (*Object, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if ctx.Simulation {
		return nil, nil
	}
	enabled, err := s.enabled(ctx)
	if err != nil || !enabled {
		return nil, err
	}
	found := false
	for _, r := range exposed {
		if r.Reference == ref {
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}
	head, err := s.reader.Head(ref.ID)
	if err != nil || head == nil {
		return nil, err
	}
	if get(head, "deleted") == true || get(head, "sceneId") != ctx.SceneID || num(get(head, "deletionEpoch")) != ref.DeletionEpoch {
		return nil, nil
	}
	if s.historical == nil {
		return nil, errors.New("commitment_historical_authority_required")
	}
	revisions, err := s.reader.Revisions(ref.ID)
	if err != nil {
		return nil, err
	}
	views := []any{}
	for _, raw := range revisions {
		v, err := ProjectCommitment(ctx, head, object(raw), s.historical)
		if err != nil {
			return nil, err
		}
		views = append(views, v)
	}
	return obj("revisions", views, "coverage", "limited", "interpretation", "previously_recorded_not_current_truth"), nil
}

// TemporalHistory validates the current mutation anchor before entering the
// real retained declaration timeline reader. No reconstruction/backfill occurs.
func (s *ReadService) TemporalHistory(ctx Context, anchor Exposure, exposed []Exposure, input TimelineQuery) (*Object, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if ctx.Simulation {
		return nil, nil
	}
	enabled, err := s.enabled(ctx)
	if err != nil || !enabled {
		return nil, err
	}
	found := false
	for _, r := range exposed {
		if r == anchor {
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}
	head, err := s.reader.Head(anchor.ID)
	if err != nil || head == nil {
		return nil, err
	}
	if get(head, "deleted") == true || get(head, "sceneId") != ctx.SceneID || num(get(head, "deletionEpoch")) != anchor.DeletionEpoch || num(get(head, "mutationVersion")) != anchor.MutationVersion {
		return nil, nil
	}
	current, err := s.Current(ctx, anchor.Reference)
	if err != nil || current == nil {
		return nil, err
	}
	if s.historical == nil {
		return nil, errors.New("commitment_historical_authority_required")
	}
	timeline, err := s.reader.Timeline(anchor.ID)
	if err != nil {
		return nil, err
	}
	return ReadTimeline(ctx, anchor, timeline, input, TimelinePorts{Revision: func(version float64) (*Object, error) { return s.reader.Revision(anchor.ID, version) }, Historical: s.historical})
}
