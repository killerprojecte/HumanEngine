package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type AssertionHistoryQuery struct {
	SceneID, ID           string
	AsOfRecorded, ValidAt string
	Limit                 int
}

func assertionHistoryEnabled(r *assertions.Reader, schemas *contract.Schemas) (bool, error) {
	raw, exists, err := r.SettingsInput()
	if err != nil {
		return false, err
	}
	input := []byte(raw)
	if !exists {
		input = contract.DefaultSettingsInput()
	}
	settings, err := schemas.Parse("Settings", input)
	return settings["memoryHistoricalReadEnabled"] == true, err
}

func (a *AdminRead) PublishAssertionHistory(ctx context.Context, q *AssertionHistoryQuery, schemas *contract.Schemas, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	real, enabled, allowed, admitted := false, false, false, false
	if q != nil {
		err := a.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
			admitted = true
			real, err = realPeopleScene(r, q.SceneID)
			if err != nil || !real {
				return err
			}
			enabled, err = assertionHistoryEnabled(r, schemas)
			return err
		})
		if err != nil {
			return admitted, err
		}
		if enabled {
			allowed, err = a.memory.prepareHistoricalAnchor(ctx, Context{SceneID: q.SceneID}, q.ID)
			if err != nil {
				return true, err
			}
		}
	}
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		if q == nil {
			status = http.StatusBadRequest
			return []byte(`{"error":"Invalid request"}`), nil
		}
		if real {
			var err error
			real, err = realPeopleScene(r, q.SceneID)
			if err != nil {
				return nil, err
			}
		}
		if !real {
			status = http.StatusNotFound
			return []byte(`{"error":"Scene unavailable"}`), nil
		}
		if allowed {
			var err error
			enabled, err = assertionHistoryEnabled(r, schemas)
			if err != nil {
				return nil, err
			}
			if enabled {
				input := personObject("memoryId", q.ID)
				if q.AsOfRecorded != "" {
					_ = input.Set("asOfRecorded", q.AsOfRecorded)
				}
				if q.ValidAt != "" {
					_ = input.Set("validAt", q.ValidAt)
				}
				if q.Limit != 0 {
					_ = input.Set("limit", float64(q.Limit))
				}
				result, err := a.memory.historicalQueryRead(r, Context{SceneID: q.SceneID}, input)
				if err == nil {
					return projectAssertionHistory(result)
				}
				if !errors.Is(err, errHistoryAnchorUnavailable) {
					return nil, err
				}
			}
		}
		status = http.StatusNotFound
		return []byte(`{"error":"History unavailable"}`), nil
	}, func(body []byte) error { return emit(status, body) })
}

func projectAssertionHistory(result *Object) ([]byte, error) {
	view := personObject("interpretation", value(result, "interpretation"), "sourceBodyCoverage", value(result, "sourceBodyCoverage"))
	for _, key := range []string{"asOfRecorded", "validAt"} {
		if v := value(result, key); !jsjson.IsUndefined(v) {
			_ = view.Set(key, v)
		}
	}
	c := object(value(result, "coverage"))
	_ = view.Set("coverage", personObject("status", value(c, "status"), "returned", value(c, "returned"), "hasMore", value(c, "hasMore")))
	rows := value(result, "revisions").([]any)
	for i, row := range rows {
		rev := object(row)
		record := object(value(rev, "record"))
		out := personObject("version", value(rev, "version"), "recordedAt", value(rev, "recordedAt"), "validFrom", value(rev, "validFrom"), "validTo", value(rev, "validTo"), "content", value(record, "content"), "status", value(record, "status"), "kind", value(record, "kind"))
		if v, ok := record.Get("epistemicKind"); ok {
			_ = out.Set("epistemicKind", v)
		}
		_ = out.Set("validityCoverage", value(rev, "validityCoverage"))
		rows[i] = out
	}
	_ = view.Set("revisions", rows)
	return jsjson.Marshal(view)
}
