package offlinelifecycle

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fixtureStep struct {
	MediaAfter         map[string]string
	Error              string
	After              map[string][]map[string]any
	Gate               recovery.Gate
	UUIDSequenceBefore int64
}
type fixtureCase struct {
	MediaBefore map[string]string
	Name        string
	Before      map[string][]map[string]any
	GateBefore  recovery.Gate
	Head        recovery.Head
	History     []struct {
		Sequence      int64
		PreviousHash  string `json:"previous_hash"`
		Payload, Hash string
	}
	Steps []fixtureStep
}
type sourceFixture struct {
	Comparisons [][]any
	Schemas     map[string]string
	Cases       []fixtureCase
}

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestActualReconcileSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/reconcile.json")
	must(t, e)
	var f sourceFixture
	must(t, json.Unmarshal(raw, &f))
	comparisonPairs := map[string]int{}
	for _, pair := range f.Comparisons {
		comparisonPairs[pair[0].(string)+"\x00"+pair[1].(string)] = int(pair[2].(float64))
	}
	for _, scenario := range f.Cases {
		t.Run(scenario.Name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			authorityPath := filepath.Join(root, "authority")
			must(t, os.Mkdir(authorityPath, 0700))
			must(t, os.Mkdir(filepath.Join(root, "stage"), 0700))
			must(t, os.Mkdir(filepath.Join(root, "media"), 0700))
			for filename, encoded := range scenario.MediaBefore {
				bytes, e := base64.StdEncoding.DecodeString(encoded)
				must(t, e)
				must(t, os.WriteFile(filepath.Join(root, "media", filename), bytes, 0600))
			}
			adb, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
			must(t, e)
			_, e = adb.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
			must(t, e)
			_, e = adb.Exec("INSERT INTO identity VALUES(?,1)", scenario.Head.InstanceID)
			must(t, e)
			for _, entry := range scenario.History {
				_, e = adb.Exec("INSERT INTO revocations VALUES(?,?,?,?)", entry.Sequence, entry.PreviousHash, entry.Payload, entry.Hash)
				must(t, e)
			}
			must(t, adb.Close())
			path := filepath.Join(root, "stage", "main.sqlite")
			db, e := sql.Open("sqlite3", path)
			must(t, e)
			defer db.Close()
			db.SetMaxOpenConns(1)
			_, e = db.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
			must(t, e)
			for table := range scenario.Before {
				_, e = db.Exec(f.Schemas[table])
				must(t, e)
			}
			for table, rows := range scenario.Before {
				for _, row := range rows {
					columns, marks, args := []string{}, []string{}, []any{}
					for key, v := range row {
						columns = append(columns, `"`+key+`"`)
						marks = append(marks, "?")
						if table == "recovery_model_hold" && key == "restore_id" {
							v = "00000000-0000-4000-8000-000000000001"
						}
						args = append(args, v)
					}
					_, e = db.Exec(`INSERT INTO "`+table+`" (`+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", args...)
					must(t, e)
				}
			}
			gate := scenario.GateBefore
			gate.Authority = authorityPath
			encoded, e := json.Marshal(gate)
			must(t, e)
			_, e = db.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(encoded))
			must(t, e)
			if scenario.Name == "unknown-semantic-schema" {
				_, e = db.Exec("CREATE TABLE memory_semantic_unknown(id TEXT)")
				must(t, e)
			}
			if scenario.Name == "final-gate-failure-restart" || scenario.Name == "mixed-all-domains-restart" {
				_, e = db.Exec(`CREATE TRIGGER final_fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.state')='ready' BEGIN SELECT RAISE(ABORT,'synthetic_final_failure'); END`)
				must(t, e)
			}
			a, e := recovery.Open(ctx, authorityPath)
			must(t, e)
			defer a.Close()
			c, e := db.Conn(ctx)
			must(t, e)
			defer c.Close()
			dates, e := jsdate.New("UTC")
			must(t, e)
			for i, step := range scenario.Steps {
				owner, admissionErr := recovery.AdmitMaintenance(ctx, c, path, a)
				if admissionErr != nil {
					if step.Error == "" || !strings.Contains(admissionErr.Error(), step.Error) {
						t.Fatal(admissionErr, step.Error)
					}
					return
				}
				sequence := step.UUIDSequenceBefore
				uuid := func() string { sequence++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence) }
				compare := func(a, b string) (int, error) {
					if a == b {
						return 0, nil
					}
					if n, ok := comparisonPairs[a+"\x00"+b]; ok {
						return n, nil
					}
					return 0, fmt.Errorf("unrecorded actual source comparison %q %q", a, b)
				}
				now := func() string { return "2026-10-28T12:00:00.000Z" }
				settingsFlag := func(key string) bool {
					var raw string
					if e := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&raw); e != nil {
						return false
					}
					var settings map[string]any
					if json.Unmarshal([]byte(raw), &settings) != nil {
						return false
					}
					return settings[key] == true
				}
				options := Options{MediaRoot: filepath.Join(root, "media"), Now: func() time.Time { return time.UnixMilli(1793188800000) }, Memory: memoryservice.OfflineLifecycleOptions{
					Assertions: assertions.Options{DateParser: dates, Now: now, OperationID: uuid}, Authority: assertionauthority.Options{DateParser: dates, Compare: compare, NowMillis: func() int64 { return 1793188800000 }}, Commitments: commitments.Options{DateParser: dates, Compare: compare, Now: now}, Projection: projections.Options{Now: func() float64 { return 1793188800000 }, Token: func() (string, error) { return uuid(), nil }}, WorkingSet: workingset.Ports{Enabled: func() bool { return settingsFlag("memoryWorkingSetEnabled") }, Now: func() float64 { return 1793188800000 }, Token: func() (string, error) { return uuid(), nil }}, ProjectionEnabled: func() bool { return settingsFlag("memoryProjectionEnabled") }, CommitmentsEnabled: func(commitments.Context) (bool, error) { return settingsFlag("memoryCommitmentsEnabled"), nil }, Log: func(a, b, c string) { t.Log(a, b, c) },
				}}
				head, e := Reconcile(ctx, owner, options)
				if step.Error != "" {
					if e == nil || !strings.Contains(e.Error(), step.Error) {
						t.Fatalf("step %d error %v want %s", i, e, step.Error)
					}
				} else {
					must(t, e)
					if head != scenario.Head {
						t.Fatal(head, scenario.Head)
					}
				}
				files, e := os.ReadDir(filepath.Join(root, "media"))
				must(t, e)
				actualMedia := map[string]string{}
				for _, file := range files {
					bytes, e := os.ReadFile(filepath.Join(root, "media", file.Name()))
					must(t, e)
					actualMedia[file.Name()] = base64.StdEncoding.EncodeToString(bytes)
				}
				if !reflect.DeepEqual(actualMedia, step.MediaAfter) {
					t.Errorf("step %d media %v want %v", i, actualMedia, step.MediaAfter)
				}
				for table, want := range step.After {
					rows, e := c.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
					must(t, e)
					columns, e := rows.Columns()
					must(t, e)
					got := []map[string]any{}
					for rows.Next() {
						values := make([]any, len(columns))
						pointers := make([]any, len(columns))
						for i := range pointers {
							pointers[i] = &values[i]
						}
						must(t, rows.Scan(pointers...))
						row := map[string]any{}
						for i, key := range columns {
							v := values[i]
							if n, ok := v.(int64); ok {
								v = float64(n)
							}
							if table == "recovery_model_hold" && key == "restore_id" {
								v = "$RESTORE_ID"
							}
							row[key] = v
						}
						got = append(got, row)
					}
					must(t, rows.Err())
					must(t, rows.Close())
					if !reflect.DeepEqual(got, want) {
						b, _ := json.Marshal(got)
						w, _ := json.Marshal(want)
						t.Errorf("step %d table %s\ngot %s\nwant %s", i, table, b, w)
					}
				}
				var rawGate string
				must(t, c.QueryRowContext(ctx, "SELECT value FROM recovery_gate WHERE id=1").Scan(&rawGate))
				var gotGate recovery.Gate
				must(t, json.Unmarshal([]byte(rawGate), &gotGate))
				wantGate := step.Gate
				wantGate.Authority = authorityPath
				if gotGate != wantGate {
					t.Errorf("gate %v want %v", gotGate, wantGate)
				}
				if (scenario.Name == "final-gate-failure-restart" || scenario.Name == "mixed-all-domains-restart") && i == 0 {
					_, e = c.ExecContext(ctx, "DROP TRIGGER final_fault")
					must(t, e)
					must(t, c.Close())
					c, e = db.Conn(ctx)
					must(t, e)
				}
			}
		})
	}
}
