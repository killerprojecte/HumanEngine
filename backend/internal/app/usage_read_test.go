package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestApplicationUsageReadsDedicatedPublisher(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	app, e := Open(context.Background(), options)
	if e != nil {
		if app != nil {
			app.Shutdown(context.Background())
		}
		t.Fatal(e)
	}
	defer app.Shutdown(context.Background())
	raw, e := os.ReadFile("../usageview/testdata/http-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Seed struct{ Events []map[string]any }
	}
	if e = json.Unmarshal(raw, &source); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", options.Installation.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	row := source.Seed.Events[0]
	keys, args := []string{}, []any{}
	for k, v := range row {
		keys = append(keys, k)
		args = append(args, v)
	}
	if _, e = db.Exec("INSERT INTO model_usage_events ("+strings.Join(keys, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+")", args...); e != nil {
		t.Fatal(e)
	}
	values := appRequest(t, app, "GET", "/api/usage?start=2026-11-01T00%3A00%3A00Z&end=2026-11-02T00%3A00%3A00Z&bucket=hour&timeZone=America%2FNew_York", nil).([]any)
	if len(values) != 1 || field(values[0], "day") != "2026-11-01T05:00:00.000Z" || field(values[0], "role") != "replyer" {
		t.Fatal(values)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("read unexpectedly started Node")
	}
}
