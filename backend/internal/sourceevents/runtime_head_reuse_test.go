package sourceevents

import (
	"errors"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/mattn/go-sqlite3"
)

func TestValidateSourcesReusesHeadOnlyInStableSnapshot(t *testing.T) {
	f, authority := runtimeFixture(t, Options{})
	data := runtimeFixtureData(t)
	scene := sceneFromValue(runtimeObject(t, data.Scene))
	runtimePut(t, f.main, "scenes", scene.ID, data.Scene)
	runtimePut(t, f.main, "config", "settings", data.Cases[1].Initial.Settings)
	status, trusted, err := authority.Observe(t.Context(), scene, runtimeObject(t, data.Bodies[0].Message), nil)
	if err != nil || status != "new" || get(trusted, "sourceEventRef") == nil {
		t.Fatalf("versioned source: %q, %v", status, err)
	}
	f.main.SetMaxOpenConns(1)
	conn, err := f.main.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var heads, revisions int
	if err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, table, column, _ string) int {
			if op == sqlite3.SQLITE_READ && table == "source_event_heads" && column == "event_id" {
				heads++
			}
			if op == sqlite3.SQLITE_READ && table == "source_event_revisions" && column == "value" {
				revisions++
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	rows := []*jsjson.Object{trusted}
	check := func(reads botruntime.ReadAuthority, wantHeads int) {
		t.Helper()
		heads, revisions = 0, 0
		valid, err := reads.ValidateSources(scene, rows, nil)
		if !valid || err != nil || heads != wantHeads || revisions != 1 {
			t.Fatalf("valid=%v error=%v head queries=%d revision queries=%d; want true, nil, %d, 1", valid, err, heads, revisions, wantHeads)
		}
	}
	check(authority, 1)
	if err = f.authority.WithWriteSession(t.Context(), f.main, func(ws *recovery.WriteSession) error {
		return f.store.WithSession(ws).WithRuntimeReads(t.Context(), func(reads botruntime.ReadAuthority) error {
			// The writer connection is owned separately from the main pool.
			// Count its SQL at the true scoped query boundary instead.
			v := reads.(*runtimeReads).view
			query := v.query
			v.query = func(sql string, args ...any) (Rows, error) {
				if strings.Contains(sql, "FROM source_event_heads WHERE identity_key=") {
					heads++
				}
				if strings.Contains(sql, "FROM source_event_revisions WHERE event_id=") {
					revisions++
				}
				return query(sql, args...)
			}
			check(reads, 2)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.main.Exec("UPDATE source_event_revisions SET value='{}'"); err != nil {
		t.Fatal(err)
	}
	if valid, err := authority.ValidateSources(scene, rows, nil); valid || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("reused head concealed an invalid revision: valid=%v error=%v", valid, err)
	}
}
