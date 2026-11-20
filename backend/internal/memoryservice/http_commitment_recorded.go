package memoryservice

import (
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var commitmentHistoryUnavailable = []byte(`{"error":"History unavailable"}`)

func commitmentRecorded(q CommitmentAdminQuery, c commitments.Context, read *commitments.ReadService, names *peopleNames) ([]byte, int, error) {
	var result *Object
	var err error
	anchor := commitments.Exposure{Reference: q.Ref, MutationVersion: q.AnchorMutation}
	if q.Recorded == CommitmentRevisionHistory {
		var current *Object
		current, err = read.Current(c, q.Ref)
		if err != nil {
			return nil, 0, err
		}
		if current == nil {
			return []byte(`{"error":"Commitment unavailable"}`), http.StatusNotFound, nil
		}
		anchor.MutationVersion = value(current, "mutationVersion").(float64)
		result, err = read.History(c, q.Ref, []commitments.Exposure{anchor})
		if err == nil && result != nil {
			revisions := value(result, "revisions").([]any)
			revisions = revisions[max(0, len(revisions)-20):]
			if err := projectCommitmentAdminItems(revisions, names); err != nil {
				return nil, 0, err
			}
			_ = result.Set("revisions", revisions)
			_ = result.Set("sourceBodyCoverage", "unavailable")
		}
	} else {
		result, err = read.TemporalHistory(c, anchor, []commitments.Exposure{anchor}, q.Timeline)
		if err == nil && result != nil {
			for _, entry := range value(result, "recordedStates").([]any) {
				if err := projectCommitmentAdminItems(value(object(entry), "states").([]any), names); err != nil {
					return nil, 0, err
				}
			}
		}
	}
	if err != nil {
		return nil, 0, err
	}
	if result == nil {
		return commitmentHistoryUnavailable, http.StatusNotFound, nil
	}
	body, err := jsjson.Marshal(result)
	return body, http.StatusOK, err
}
