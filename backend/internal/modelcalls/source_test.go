package modelcalls

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "github.com/mattn/go-sqlite3"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// Run the actual source metadata owner and use its initialized SQL schema.
func TestActualSourceRecordAndPagination(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script := `
import Database from '../../../node_modules/better-sqlite3/lib/index.js';
import {initializeModelCalls,recordModelCall,modelCalls} from '../../../src/model-calls.ts';
const now='2026-01-02T00:00:00.000Z';Date.now=()=>Date.parse(now);
const db=new Database(':memory:');initializeModelCalls(db);
const entries=[
{at:now,modelId:'m',providerId:'p',role:'thinker',event:'model.completed',attempt:1,durationMs:20,usage:{inputTokens:2,outputTokens:4,cacheReadInputTokens:0,cacheWriteInputTokens:0}},
{at:now,modelId:'m',providerId:'p',role:'vision',event:'model.failed',attempt:2,durationMs:-1,transportMode:'stream',firstTokenMs:0,firstTokenKind:'text',failureReason:'stream_interrupted',usage:{known:false,inputTokens:10,outputTokens:20}},
{at:now,modelId:'m',providerId:'p',role:'models',event:'model.completed',attempt:1,durationMs:10},
{at:'2020-01-01T00:00:00.000Z',modelId:'m',providerId:'p',role:'planner',event:'model.failed',attempt:1,durationMs:0}];
for(const e of entries)recordModelCall(db,e);
const page=modelCalls(db,{limit:1});const next=modelCalls(db,{limit:1,beforeId:page.nextCursor});
const schema=db.prepare("SELECT sql FROM sqlite_master WHERE name='model_calls'").get().sql;
process.stdout.write(JSON.stringify({schema,page,next}));db.close();`
	raw, err := exec.Command(node, "--import", "tsx", "--input-type=module", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("source owner: %v %s", err, raw)
	}
	var expected struct {
		Schema     string
		Page, Next Page
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(expected.Schema); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	n := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	entries := []Observation{
		{At: now.Format("2006-01-02T15:04:05.000Z"), ModelID: "m", ProviderID: "p", Role: "thinker", Event: "model.completed", Attempt: 1, DurationMS: 20, Usage: &Usage{Known: true, Input: n(2), Output: n(4), CacheRead: n(0), CacheWrite: n(0)}},
		{At: now.Format("2006-01-02T15:04:05.000Z"), ModelID: "m", ProviderID: "p", Role: "vision", Event: "model.failed", Attempt: 2, DurationMS: -1, TransportMode: "stream", FirstTokenMS: n(0), FirstTokenKind: str("text"), FailureReason: str("stream_interrupted"), Usage: &Usage{Known: false, Input: n(10), Output: n(20)}},
		{At: now.Format("2006-01-02T15:04:05.000Z"), ModelID: "m", ProviderID: "p", Role: "models", Event: "model.completed", Attempt: 1, DurationMS: 10},
		{At: "2020-01-01T00:00:00.000Z", ModelID: "m", ProviderID: "p", Role: "planner", Event: "model.failed", Attempt: 1},
	}
	for _, e := range entries {
		if err = store.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	page, err := store.QueryOn(context.Background(), conn, Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.QueryOn(context.Background(), conn, Query{Limit: 1, BeforeID: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page, expected.Page) || !reflect.DeepEqual(next, expected.Next) {
		t.Fatalf("source mismatch: got %#v %#v; expected %#v %#v", page, next, expected.Page, expected.Next)
	}
}
