package learningowner

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const indexRebuildLeaseMillis = 30_000

type indexRebuildLease struct {
	job, attempt, owner string
}

func (l indexRebuildLease) identifies(job *Object) bool {
	return str(job, "id") == l.job && str(job, "attemptId") == l.attempt && str(job, "ownerId") == l.owner
}

func indexJobInSession(ws *recovery.WriteSession) (*Object, error) {
	rows, err := ws.Query("SELECT value FROM config WHERE key='embedding.rebuild'")
	if err != nil {
		return nil, err
	}
	var raw string
	found := rows.Next()
	if found {
		err = rows.Scan(&raw)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil || !found {
		return nil, err
	}
	decoded, err := jsjson.Parse(raw)
	return indexObject(decoded), err
}

func checkExpectedIndexJob(job *Object, expected *string) error {
	if (job == nil && expected != nil) || (job != nil && (expected == nil || str(job, "id") != *expected)) {
		return errors.New("embedding_job_changed")
	}
	return nil
}

func (o *Owner) indexRebuildState(ws *recovery.WriteSession, expected *string) (indexState, error) {
	state, err := readIndexState(func(q string, args ...any) (indexRows, error) { return ws.Query(q, args...) }, false)
	if err != nil {
		return indexState{}, err
	}
	if err := checkExpectedIndexJob(state.job, expected); err != nil {
		return indexState{}, err
	}
	return state, o.expireIndexLease(ws, state.job)
}

// submitIndexRebuildInSession creates only a durable staging job. It does not
// launch a worker, alter the active generation, or initiate a provider call.
func (o *Owner) submitIndexRebuildInSession(ws *recovery.WriteSession, expected *string, revision string, lease indexRebuildLease, total int) (*Object, error) {
	state, err := o.indexRebuildState(ws, expected)
	if err != nil {
		return nil, err
	}
	if str(state.job, "state") == "running" {
		return nil, errors.New("embedding_rebuild_busy")
	}
	if state.selected == nil {
		return nil, errors.New("embedding_model_not_configured")
	}
	if state.selected.revision != revision {
		return nil, errors.New("embedding_config_changed")
	}
	if err := ws.EnsureEmbeddingRebuildSchema(); err != nil {
		return nil, err
	}
	job := obj("id", lease.job, "encoderRevision", revision, "modelId", state.selected.model.ID,
		"state", "running", "processed", float64(0), "total", float64(total),
		"requiresRetryConfirmation", false)
	if state.active != nil {
		_ = job.Set("expectedActive", str(state.active, "generationId"))
	}
	_ = job.Set("attemptId", lease.attempt)
	_ = job.Set("ownerId", lease.owner)
	return job, renewIndexRebuildInSession(ws, job, o.d.Now().UnixMilli())
}

func (o *Owner) resumeIndexRebuildInSession(ws *recovery.WriteSession, expected *string, lease indexRebuildLease, confirmed bool) (*Object, error) {
	state, err := o.indexRebuildState(ws, expected)
	if err != nil {
		return nil, err
	}
	job := state.job
	if s := str(job, "state"); s != "paused" && s != "failed" {
		return nil, errors.New("embedding_rebuild_not_resumable")
	}
	if state.selected == nil || state.selected.revision != str(job, "encoderRevision") {
		return nil, errors.New("embedding_config_changed")
	}
	if get(job, "requiresRetryConfirmation") == true && !confirmed {
		return nil, errors.New("embedding_retry_confirmation_required")
	}
	if err := ws.EnsureEmbeddingRebuildSchema(); err != nil {
		return nil, err
	}
	_ = job.Set("state", "running")
	job.Delete("error")
	_ = job.Set("requiresRetryConfirmation", false)
	_ = job.Set("attemptId", lease.attempt)
	_ = job.Set("ownerId", lease.owner)
	return job, renewIndexRebuildInSession(ws, job, o.d.Now().UnixMilli())
}

func stopIndexRebuildInSession(ws *recovery.WriteSession, expected *string, cancel bool) (*Object, error) {
	job, err := indexJobInSession(ws)
	if err != nil {
		return nil, err
	}
	if err := checkExpectedIndexJob(job, expected); err != nil {
		return nil, err
	}
	if s := str(job, "state"); s != "running" && s != "paused" && s != "failed" {
		return nil, errors.New("embedding_rebuild_not_running")
	}
	state := "paused"
	if cancel {
		state = "cancelled"
	}
	_ = job.Set("state", state)
	return job, saveIndexValue(ws, "embedding.rebuild", job)
}

func pauseOwnedIndexRebuildInSession(ws *recovery.WriteSession, lease indexRebuildLease) (bool, error) {
	job, err := indexJobInSession(ws)
	if err != nil || str(job, "state") != "running" || !lease.identifies(job) {
		return false, err
	}
	_ = job.Set("state", "paused")
	err = saveIndexValue(ws, "embedding.rebuild", job)
	return err == nil, err
}

func ownedIndexRebuildInSession(ws *recovery.WriteSession, lease indexRebuildLease, now int64) (*Object, error) {
	job, err := indexJobInSession(ws)
	if err != nil || str(job, "state") != "running" || !lease.identifies(job) {
		return nil, err
	}
	if expiry, _ := get(job, "leaseUntil").(float64); expiry <= float64(now) {
		return nil, nil
	}
	return job, nil
}

func renewIndexRebuildInSession(ws *recovery.WriteSession, job *Object, now int64) error {
	_ = job.Set("leaseUntil", float64(now+indexRebuildLeaseMillis))
	return saveIndexValue(ws, "embedding.rebuild", job)
}

// markIndexRebuildUncertain precedes dispatch of a charged batch. A heartbeat
// uses the same ownership/lease check but does not alter uncertainty.
func markIndexRebuildUncertain(ws *recovery.WriteSession, lease indexRebuildLease, now int64) (bool, error) {
	job, err := ownedIndexRebuildInSession(ws, lease, now)
	if err != nil || job == nil {
		return false, err
	}
	_ = job.Set("requiresRetryConfirmation", true)
	err = renewIndexRebuildInSession(ws, job, now)
	return err == nil, err
}

type indexCheckpoint struct{ id, content string }

// checkpointIndexRebuild borrows the caller's vector write transaction. Only a
// vector already present in the same staging space can back a checkpoint.
// No published generation is changed by this phase.
func checkpointIndexRebuild(ws *recovery.WriteSession, lease indexRebuildLease, now int64, space string, dimensions, processed, total int, written []indexCheckpoint) (bool, error) {
	job, err := ownedIndexRebuildInSession(ws, lease, now)
	if err != nil || job == nil {
		return false, err
	}
	if existing := str(job, "spaceId"); existing != "" && (existing != space || get(job, "dimensions") != float64(dimensions)) {
		return false, errors.New("embedding_dimensions_changed")
	}
	for _, item := range written {
		hash, err := indexHash([]any{item.id, item.content})
		if err != nil {
			return false, err
		}
		res, err := ws.Exec(`INSERT INTO embedding_rebuild_items (job_id,memory_id,content_hash)
			SELECT ?,v.memory_id,? FROM memory_vectors v JOIN vector_spaces s ON s.id=v.space_id
			WHERE v.memory_id=? AND v.space_id=? AND json_extract(s.value,'$.generation')=?
			ON CONFLICT(job_id,memory_id) DO UPDATE SET content_hash=excluded.content_hash`, lease.job, hash, item.id, space, lease.job)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return false, errors.Join(err, errors.New("embedding_checkpoint_vector_missing"))
		}
	}
	_ = job.Set("processed", float64(processed))
	_ = job.Set("total", float64(total))
	_ = job.Set("dimensions", float64(dimensions))
	_ = job.Set("spaceId", space)
	_ = job.Set("requiresRetryConfirmation", false)
	err = renewIndexRebuildInSession(ws, job, now)
	return err == nil, err
}
