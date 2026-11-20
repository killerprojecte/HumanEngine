package learningowner

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestEmbeddingRebuildStorageActualSource(t *testing.T) {
	var fixture struct {
		Node             string
		SourceHashes     map[string]string
		Clock            int64
		Config, Previous json.RawMessage
		Inputs           []string
		Tables           map[string]string
		Cases            []struct{ Name, Error string }
		Snapshots        []struct {
			Name, Active string
			Now          int64
			Job          map[string]any
			Checkpoints  []map[string]string
		}
		Writes []map[string]any
	}
	raw, err := os.ReadFile("testdata/embedding-rebuild-storage-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Snapshots) != 7 || len(fixture.Cases) != 7 {
		t.Fatalf("fixture invalid: %s %v", fixture.Node, err)
	}
	for path, digest := range fixture.SourceHashes {
		action, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(action)) != digest {
			t.Fatalf("source drift %s: %v", path, err)
		}
	}
	f := scopedSetup(t)
	ctx := t.Context()
	head, err := f.recovery.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := fixture.Clock
	o := &Owner{d: Dependencies{Main: f.main, Authority: f.recovery, Now: func() time.Time { return time.UnixMilli(now) }}}
	writable := func(fn func(*recovery.WriteSession) error) error {
		return f.recovery.WithWriteSession(ctx, f.main, fn)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.main.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, content := range fixture.Inputs {
		id := fmt.Sprintf("memory-%d", i)
		exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,'scene:s',NULL,?,?,?)", id, content, id, `{}`)
	}
	exec("INSERT INTO config VALUES('models',?),('embedding.active',?)", string(fixture.Config), string(fixture.Previous))
	readObject := func(key string) map[string]any {
		t.Helper()
		var s string
		if err := f.main.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", key).Scan(&s); err != nil {
			t.Fatal(err)
		}
		var o map[string]any
		if err := json.Unmarshal([]byte(s), &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	selected, err := selectEncoder(parseObject(t, string(fixture.Config)))
	if err != nil || selected == nil {
		t.Fatalf("config: %+v %v", selected, err)
	}
	lease0 := indexRebuildLease{"job-0", "attempt-0", "owner-0"}
	lease1 := indexRebuildLease{"job-0", "attempt-1", "owner-1"}
	wrong := "not-latest"
	jobID := lease0.job
	reject := func(name string, fn func(*recovery.WriteSession) error) {
		t.Helper()
		err := writable(fn)
		for _, c := range fixture.Cases {
			if c.Name == name {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("%s: got %v want %s", name, err, c.Error)
				}
				return
			}
		}
		t.Fatalf("missing rejected case %s", name)
	}
	writes := 0
	write := func(fn func(*recovery.WriteSession) error) {
		t.Helper()
		if err := writable(fn); err != nil {
			t.Fatal(err)
		}
		got := readObject("embedding.rebuild")
		if want := fixture.Writes[writes]; !reflect.DeepEqual(got, want) {
			t.Fatalf("write %d: got %v want %v", writes, got, want)
		}
		writes++
	}
	observe := func(name string) {
		t.Helper()
		for _, want := range fixture.Snapshots {
			if want.Name != name {
				continue
			}
			job := readObject("embedding.rebuild")
			active := readObject("embedding.active")["generationId"]
			if now != want.Now || active != want.Active || !reflect.DeepEqual(job, want.Job) {
				t.Fatalf("%s: time %d active %v job %v; want %d %s %v", name, now, active, job, want.Now, want.Active, want.Job)
			}
			rows, err := f.main.QueryContext(ctx, "SELECT job_id,memory_id,content_hash FROM embedding_rebuild_items ORDER BY memory_id")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			got := []map[string]string{}
			for rows.Next() {
				var job, id, hash string
				if err := rows.Scan(&job, &id, &hash); err != nil {
					t.Fatal(err)
				}
				got = append(got, map[string]string{"job_id": job, "memory_id": id, "content_hash": hash})
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want.Checkpoints) {
				t.Fatalf("%s: checkpoints %v want %v", name, got, want.Checkpoints)
			}
			return
		}
		t.Fatalf("missing snapshot %s", name)
	}
	submit := func(expected *string, revision string) func(*recovery.WriteSession) error {
		return func(ws *recovery.WriteSession) error {
			_, err := o.submitIndexRebuildInSession(ws, expected, revision, lease0, len(fixture.Inputs))
			return err
		}
	}
	ownedNecessary := func(ok bool, err error) error {
		if err == nil && !ok {
			return errors.New("expected current lease")
		}
		return err
	}
	mark := func(lease indexRebuildLease) func(*recovery.WriteSession) error {
		return func(ws *recovery.WriteSession) error {
			return ownedNecessary(markIndexRebuildUncertain(ws, lease, now))
		}
	}
	resume := func(confirmed bool) func(*recovery.WriteSession) error {
		return func(ws *recovery.WriteSession) error {
			_, err := o.resumeIndexRebuildInSession(ws, &jobID, lease1, confirmed)
			return err
		}
	}
	stop := func(expected *string, cancel bool) func(*recovery.WriteSession) error {
		return func(ws *recovery.WriteSession) error {
			_, err := stopIndexRebuildInSession(ws, expected, cancel)
			return err
		}
	}

	reject("wrong-latest-before-start", submit(&wrong, selected.revision))
	reject("wrong-encoder-before-start", submit(nil, "not-current"))
	write(submit(nil, selected.revision))
	write(mark(lease0))
	observe("submitted-paid-request-uncertain")
	reject("busy-local-owner", submit(&jobID, selected.revision))
	reject("pause-stale-job", stop(&wrong, false))
	now += 30_001
	if err := o.initializeIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if job := readObject("embedding.rebuild"); !reflect.DeepEqual(job, fixture.Writes[writes]) {
		t.Fatalf("expired restart: %v", job)
	}
	writes++
	observe("expired-restart-no-replay")
	reject("unconfirmed-resume", resume(false))
	write(resume(true))
	write(mark(lease1))
	observe("resumed-attempt-uncertain")
	if err := writable(func(ws *recovery.WriteSession) error {
		if ok, err := pauseOwnedIndexRebuildInSession(ws, lease0); ok || err != nil {
			return fmt.Errorf("stale pause %t %v", ok, err)
		}
		if ok, err := markIndexRebuildUncertain(ws, lease0, now); ok || err != nil {
			return fmt.Errorf("stale handoff %t %v", ok, err)
		}
		ok, err := checkpointIndexRebuild(ws, lease0, now, "stale", 2, 1, 2, nil)
		if ok || err != nil {
			return fmt.Errorf("stale checkpoint %t %v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	observe("stale-attempt-cannot-advance-or-pause-peer")
	item := indexCheckpoint{"memory-1", fixture.Inputs[1]}
	checkpointWrite := func(ws *recovery.WriteSession, invalid bool) error {
		if _, err := ws.Exec("INSERT INTO vector_spaces VALUES('staging-space',?)", `{"generation":"job-0"}`); err != nil {
			return err
		}
		if _, err := ws.Exec("INSERT INTO memory_vectors VALUES(?, 'staging-space', ?)", item.id, []byte{0, 0, 128, 63, 0, 0, 0, 0}); err != nil {
			return err
		}
		batch := []indexCheckpoint{item}
		if invalid {
			batch = append(batch, indexCheckpoint{"memory-0", fixture.Inputs[0]})
		}
		return ownedNecessary(checkpointIndexRebuild(ws, lease1, now, "staging-space", 2, 1, 2, batch))
	}
	now += 1000
	beforeCheckpoint := readObject("embedding.rebuild")
	if err := writable(func(ws *recovery.WriteSession) error { return checkpointWrite(ws, true) }); err == nil || err.Error() != "embedding_checkpoint_vector_missing" {
		t.Fatalf("unbacked checkpoint: %v", err)
	}
	if !reflect.DeepEqual(readObject("embedding.rebuild"), beforeCheckpoint) {
		t.Fatal("rolled-back checkpoint altered job progress")
	}
	var rolledBack int
	if err := f.main.QueryRow("SELECT (SELECT count(*) FROM embedding_rebuild_items)+(SELECT count(*) FROM vector_spaces WHERE id='staging-space')").Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("failed transaction left staging material: %d %v", rolledBack, err)
	}
	write(func(ws *recovery.WriteSession) error { return checkpointWrite(ws, false) })
	write(mark(lease1))
	observe("committed-checkpoint-next-request-uncertain")
	write(stop(&jobID, false))
	observe("paused-owned-attempt")
	reject("cancel-stale-job", stop(&wrong, true))
	write(stop(&jobID, true))
	observe("cancelled-retains-committed-checkpoint")
	reject("resume-cancelled-job", resume(true))
	if writes != len(fixture.Writes) {
		t.Fatalf("write count %d", writes)
	}
	var ddl string
	if err := f.main.QueryRow("SELECT sql FROM sqlite_master WHERE name='embedding_rebuild_items'").Scan(&ddl); err != nil || ddl != fixture.Tables["embedding_rebuild_items"] {
		t.Fatalf("checkpoint DDL %q: %v", ddl, err)
	}
	if got, err := f.recovery.Head(ctx); err != nil || got != head {
		t.Fatalf("derived writes changed durable authority: %+v %v", got, err)
	}
	exec("DELETE FROM memories WHERE id=?", item.id)
	var count int
	if err := f.main.QueryRow("SELECT count(*) FROM embedding_rebuild_items").Scan(&count); err != nil || count != 0 {
		t.Fatalf("checkpoint FK retention %d: %v", count, err)
	}
	// No provider or process worker was involved in any of these transactions.
}
