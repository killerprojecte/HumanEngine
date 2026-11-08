package semantic

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Review uses the same projection qualification as lifecycle capture, with
// rejected candidates permitted only for this explicit administrator review.
// A hash or stored parent reference is never substituted for current evidence.
func (s *Store) Review(ctx Context, id string, expected float64, status string, enabled bool, a PrivacyAuthority) (*jsjson.Object, error) {
	if e := s.RequireTransaction(); e != nil {
		return nil, e
	}
	if ctx.Simulation || !enabled {
		return nil, errors.New("semantic_disabled")
	}
	current, e := s.Get(id)
	if e != nil {
		return nil, e
	}
	if current == nil || semGet(current, "sceneId") != ctx.SceneID || semGet(current, "version") != expected {
		return nil, errors.New("semantic_version_conflict")
	}
	if status == "admitted" {
		qualified, e := s.qualifyCandidates(ctx, a, []*jsjson.Object{current}, true)
		if e != nil {
			return nil, e
		}
		if len(qualified) == 0 {
			return nil, errors.New("semantic_parent_unavailable")
		}
	}
	if status != "admitted" && status != "rejected" {
		return nil, errors.New("semantic_review_invalid")
	}
	next := current.Clone()
	_ = next.Set("version", expected+1)
	_ = next.Set("status", status)
	_ = next.Set("admission", "administrator_review")
	if e = s.Write(next, current, "review"); e != nil {
		return nil, e
	}
	return next, nil
}
