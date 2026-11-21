package sourceevents

import (
	"slices"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/mattn/go-sqlite3"
)

func TestFilterUnrecalledUsesOneRealSnapshot(t *testing.T) {
	f, authority := runtimeFixture(t, Options{})
	f.main.SetMaxOpenConns(1)
	// The main snapshot begins after the authority reservation. Count actual
	// SQLite BEGINs on this connection, not fake service calls or audit misses.
	conn, err := f.main.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	if err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, verb, _, _ string) int {
			if op == sqlite3.SQLITE_TRANSACTION && verb == "BEGIN" {
				starts++
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
	if _, err = f.main.Exec(`INSERT INTO recall_tombstones VALUES ('scene','recalled','earlier'),('other','kept','earlier')`); err != nil {
		t.Fatal(err)
	}
	scene := botruntime.Scene{ID: "scene"}
	kept := runtimeObject(t, `{"id":"kept"}`)
	recalled := runtimeObject(t, `{"id":"recalled"}`)
	other := runtimeObject(t, `{"id":"tail"}`)
	rows := []*jsjson.Object{kept, recalled, kept, other}
	var separate []*jsjson.Object
	for _, row := range rows {
		recalled, err := authority.IsRecalled(scene, str(row, "id"))
		if err != nil {
			t.Fatal(err)
		}
		if !recalled {
			separate = append(separate, row)
		}
	}
	if starts != len(rows) {
		t.Fatalf("individual reads used %d snapshots, want %d", starts, len(rows))
	}
	starts = 0
	actual, err := authority.FilterUnrecalled(scene, rows)
	if err != nil || !slices.Equal(actual, separate) || starts != 1 {
		t.Fatalf("batch rows=%v snapshots=%d error=%v", actual, starts, err)
	}
}
