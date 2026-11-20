package learningowner

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type indexPendingRow struct {
	record            *Object
	id, content, hash string
}

func indexStateInSession(ws *recovery.WriteSession) (indexState, error) {
	return readIndexState(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) }, false)
}

func (o *Owner) currentIndexWork(ws *recovery.WriteSession, lease indexRebuildLease) (state indexState, current bool, err error) {
	state, err = indexStateInSession(ws)
	if err != nil {
		return
	}
	job := state.job
	now := o.d.Now().UnixMilli()
	expiry, _ := get(job, "leaseUntil").(float64)
	if str(job, "state") != "running" || !lease.identifies(job) || expiry <= float64(now) {
		return
	}
	if state.selected == nil || state.selected.revision != str(job, "encoderRevision") {
		_ = job.Set("state", "paused")
		_ = job.Set("error", "embedding_config_changed")
		return state, false, renewIndexRebuildInSession(ws, job, now)
	}
	return state, true, nil
}

func (o *Owner) pendingIndexWork(ctx context.Context, ws *recovery.WriteSession, job *Object) ([]indexPendingRow, int, error) {
	records, err := o.d.Memory.IndexableInSession(ctx, ws)
	if err != nil {
		return nil, 0, err
	}
	indexed := map[string]string{}
	if space := str(job, "spaceId"); space != "" {
		rows, err := ws.Query(`SELECT c.memory_id,c.content_hash FROM embedding_rebuild_items c
			JOIN memory_vectors v ON v.memory_id=c.memory_id AND v.space_id=? WHERE c.job_id=?`, space, str(job, "id"))
		if err != nil {
			return nil, 0, err
		}
		for rows.Next() {
			var id, hash string
			if err = rows.Scan(&id, &hash); err != nil {
				break
			}
			indexed[id] = hash
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return nil, 0, err
		}
	}
	out := make([]indexPendingRow, 0, len(records))
	for _, record := range records {
		id, content := str(record, "id"), str(record, "content")
		hash, err := indexHash([]any{id, content})
		if err != nil {
			return nil, 0, err
		}
		if indexed[id] != hash {
			out = append(out, indexPendingRow{record, id, content, hash})
		}
	}
	return out, len(records), nil
}

func (o *Owner) indexWorkLoop(ctx context.Context, lease indexRebuildLease, selected *selectedEncoder, started func(), embed func(context.Context, []string) (*providertransport.EmbeddingResult, error)) error {
	batchSize := 4
	if selected.model.BatchSize != nil {
		batchSize = *selected.model.BatchSize
	}
	for ctx.Err() == nil {
		var batch []indexPendingRow
		proceed := false
		err := o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
			state, ok, err := o.currentIndexWork(ws, lease)
			if err != nil || !ok {
				return err
			}
			pending, total, err := o.pendingIndexWork(ctx, ws, state.job)
			if err != nil {
				return err
			}
			if len(pending) == 0 {
				return o.publishIndexWork(ws, state, selected, total)
			}
			batch = pending[:min(len(pending), batchSize)]
			proceed = true
			return nil
		})
		if err != nil || !proceed {
			return err
		}
		records := make([]*Object, len(batch))
		texts := make([]string, len(batch))
		for i, row := range batch {
			records[i], texts[i] = row.record, row.content
		}
		guarded, guard, err := o.indexBatchGuard(ctx, records)
		if err != nil {
			return err
		}
		reserved, retry := false, false
		err = o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
			state, ok, err := o.currentIndexWork(ws, lease)
			if err != nil || !ok {
				return err
			}
			if err := guard(ws); err != nil {
				return err
			}
			current, total, err := o.pendingIndexWork(ctx, ws, state.job)
			if err != nil {
				return err
			}
			if len(current) < len(batch) {
				retry = true
				return nil
			}
			for i, row := range batch {
				if current[i].id != row.id || current[i].hash != row.hash {
					retry = true
					return nil
				}
			}
			now := o.d.Now().UnixMilli()
			_ = state.job.Set("processed", float64(total-len(current)))
			_ = state.job.Set("total", float64(total))
			_ = state.job.Set("requiresRetryConfirmation", true)
			err = renewIndexRebuildInSession(ws, state.job, now)
			reserved = err == nil
			return err
		})
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		if !reserved {
			return nil
		}
		started()
		if err := guard(nil); err != nil {
			return err
		}
		response, err := embed(guarded, texts)
		if err != nil {
			return err
		}
		if err := guard(nil); err != nil {
			return err
		}
		if response == nil || len(response.Vectors) != len(batch) || len(response.Vectors[0]) == 0 {
			return errors.New("invalid_embedding_response")
		}
		dimensions := len(response.Vectors[0])
		wrote := false
		err = o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
			state, ok, err := o.currentIndexWork(ws, lease)
			if err != nil || !ok {
				return err
			}
			if err := guard(ws); err != nil {
				return err
			}
			space := str(state.job, "spaceId")
			if space == "" {
				space, err = registerIndexGeneration(ws, selected, lease.job, dimensions)
				if err != nil {
					return err
				}
			} else if get(state.job, "dimensions") != float64(dimensions) {
				return errors.New("embedding_dimensions_changed")
			}
			current, total, err := o.pendingIndexWork(ctx, ws, state.job)
			if err != nil {
				return err
			}
			wanted := make(map[string]string, len(current))
			for _, row := range current {
				wanted[row.id] = row.hash
			}
			checkpoints := []indexCheckpoint{}
			for i, row := range batch {
				if wanted[row.id] != row.hash {
					continue
				}
				if err := o.d.Memory.IndexInSession(ctx, ws, row.id, space, response.Vectors[i]); err != nil {
					return err
				}
				checkpoints = append(checkpoints, indexCheckpoint{row.id, row.content})
			}
			wrote, err = checkpointIndexRebuild(ws, lease, o.d.Now().UnixMilli(), space, dimensions, total-len(current)+len(checkpoints), total, checkpoints)
			if err == nil && !wrote {
				return errors.New("embedding_lease_lost")
			}
			return err
		})
		if err != nil || !wrote {
			return err
		}
	}
	return ctx.Err()
}

// publishIndexWork is called only after a same-session coverage proof. The
// job, vector writes, previous retention and active pointer commit together.
func (o *Owner) publishIndexWork(ws *recovery.WriteSession, state indexState, selected *selectedEncoder, total int) error {
	job := state.job
	now := o.d.Now().UnixMilli()
	expiry, _ := get(job, "leaseUntil").(float64)
	if expiry <= float64(now) {
		return nil
	}
	if str(state.active, "generationId") != str(job, "expectedActive") {
		return errors.New("embedding_active_generation_changed")
	}
	space := str(job, "spaceId")
	dimensions, _ := get(job, "dimensions").(float64)
	if space == "" {
		if selected.model.Dimensions == nil {
			_ = job.Set("state", "paused")
			_ = job.Set("error", "embedding_empty_native_dimension_unknown")
			return renewIndexRebuildInSession(ws, job, now)
		}
		dimensions = float64(*selected.model.Dimensions)
		var err error
		space, err = registerIndexGeneration(ws, selected, str(job, "id"), int(dimensions))
		if err != nil {
			return err
		}
		_ = job.Set("dimensions", dimensions)
		_ = job.Set("spaceId", space)
	}
	active := obj("generationId", str(job, "id"), "encoderRevision", str(job, "encoderRevision"), "modelId", str(job, "modelId"), "dimensions", dimensions, "spaceId", space)
	if err := saveIndexValue(ws, "embedding.previousSpace", str(state.active, "spaceId")); err != nil {
		return err
	}
	if err := saveIndexValue(ws, "embedding.active", active); err != nil {
		return err
	}
	if err := saveIndexValue(ws, "embedding.incompatible", ""); err != nil {
		return err
	}
	_ = job.Set("state", "completed")
	_ = job.Set("processed", float64(total))
	_ = job.Set("total", float64(total))
	_ = job.Set("requiresRetryConfirmation", false)
	if err := renewIndexRebuildInSession(ws, job, now); err != nil {
		return err
	}
	return pruneIndexGenerations(ws, active, job)
}

func registerIndexGeneration(ws *recovery.WriteSession, selected *selectedEncoder, generation string, dimensions int) (string, error) {
	if dimensions < 1 || dimensions > 16384 || selected.provider.ID == "" || selected.model.ID == "" || selected.model.APIModelName == "" || selected.provider.BaseURL == "" {
		return "", errors.New("invalid_vector_space")
	}
	space := obj("providerId", selected.provider.ID, "modelId", selected.model.ID, "apiModelName", selected.model.APIModelName,
		"deployment", selected.provider.BaseURL, "dimensions", float64(dimensions), "preprocessing", "text-v1", "normalized", false,
		"generation", generation, "encoderRevision", selected.revision)
	id, err := indexHash(space)
	if err != nil {
		return "", err
	}
	raw, err := jsjson.Stringify(space)
	if err != nil {
		return "", err
	}
	_, err = ws.Exec("INSERT OR IGNORE INTO vector_spaces VALUES(?,?)", id, raw)
	return id, err
}

func pruneIndexGenerations(ws *recovery.WriteSession, active, job *Object) error {
	rows, err := ws.Query(`SELECT id,json_extract(value,'$.generation') FROM vector_spaces WHERE
		json_extract(value,'$.generation') IS NOT NULL AND id NOT IN
		(?,?,COALESCE((SELECT json_extract(value,'$') FROM config WHERE key='embedding.previousSpace'),''))`,
		str(active, "spaceId"), str(job, "spaceId"))
	if err != nil {
		return err
	}
	var forget [][2]string
	for rows.Next() {
		var row [2]string
		if err = rows.Scan(&row[0], &row[1]); err != nil {
			break
		}
		forget = append(forget, row)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, row := range forget {
		if _, err = ws.Exec("DELETE FROM vector_spaces WHERE id=?", row[0]); err != nil {
			return err
		}
		if _, err = ws.Exec("DELETE FROM embedding_rebuild_items WHERE job_id=?", row[1]); err != nil {
			return err
		}
	}
	return nil
}
