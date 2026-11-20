package memoryservice

import (
	"context"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type CommitmentAdminQuery struct {
	SceneID string
	List    commitments.ReadOptions
	Ref     commitments.Reference
}

type CommitmentAdminRead func(context.Context, *CommitmentAdminQuery, func(int, []byte) error) (bool, error)

func (s *Service) BindCommitmentAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (CommitmentAdminRead, error) {
	publish, err := s.d.Commitments.BindPublishedCurrent(publisher, s.d.Assertions, s.d.Authority)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q *CommitmentAdminQuery, emit func(int, []byte) error) (bool, error) {
		started, enabled := false, false
		status := http.StatusOK
		err := publish(ctx, func(r *assertions.Reader, _ commitments.Context) (bool, error) {
			raw, exists, err := r.SettingsInput()
			if err != nil {
				return false, err
			}
			input := []byte(raw)
			if !exists {
				input = contract.DefaultSettingsInput()
			}
			settings, err := schemas.Parse("Settings", input)
			enabled = settings["memoryCommitmentsEnabled"] == true
			return enabled, err
		}, func(r *assertions.Reader, read *commitments.ReadService) ([]byte, error) {
			started = true
			if q == nil {
				status = http.StatusBadRequest
				return []byte(`{"error":"Invalid request"}`), nil
			}
			real, err := realPeopleScene(r, q.SceneID)
			if err != nil {
				return nil, err
			}
			if !real {
				status = http.StatusNotFound
				return []byte(`{"error":"Scene unavailable"}`), nil
			}
			c := commitments.Context{SceneID: q.SceneID}
			names := peopleNames{r, q.SceneID, map[string]string{}}
			if q.Ref.ID != "" {
				item, err := read.Current(c, q.Ref)
				if err != nil {
					return nil, err
				}
				if item == nil {
					status = http.StatusNotFound
					return []byte(`{"error":"Commitment unavailable"}`), nil
				}
				view, err := commitmentAdminView(item, &names)
				if err != nil {
					return nil, err
				}
				return jsjson.Marshal(view)
			}
			result, err := read.List(c, q.List)
			if err != nil {
				return nil, err
			}
			items := value(result, "items").([]any)
			for i, item := range items {
				items[i], err = commitmentAdminView(object(item), &names)
				if err != nil {
					return nil, err
				}
			}
			return jsjson.Marshal(personObject("sceneId", q.SceneID, "enabled", enabled, "items", items, "coverage", value(result, "coverage")))
		}, func(body []byte) error { return emit(status, body) })
		return started, err
	}, nil
}

// Commitment reads return validated terms and typed qualification from the
// authority. This projection publishes only the public view, never evidence.
func commitmentAdminView(row *Object, names *peopleNames) (*Object, error) {
	view := new(Object)
	for _, key := range []string{"ref", "mutationVersion", "terms", "recordedAt", "confirmation", "lifecycle", "qualification", "sourceBodyCoverage", "changed"} {
		if v, found := row.Get(key); found {
			_ = view.Set(key, v)
		}
	}
	ids := []string{}
	participants := map[string]uint8{}
	for i, key := range []string{"participantIds", "confirmedParticipantIds", "missingParticipantIds", "completedParticipantIds", "cancelledParticipantIds"} {
		base := row
		if i == 0 {
			base = object(value(row, "terms"))
		}
		values, _ := value(base, key).([]any)
		for _, entry := range values {
			id := entry.(string)
			if _, exists := participants[id]; !exists {
				ids = append(ids, id)
			}
			participants[id] |= 1 << i
		}
	}
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		flags := participants[id]
		p := personObject("personId", id, "confirmed", flags&2 != 0, "missing", flags&4 != 0, "completed", flags&8 != 0, "cancelled", flags&16 != 0)
		if err := names.set(p, "displayName", id); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	_ = view.Set("participants", out)
	return view, nil
}
