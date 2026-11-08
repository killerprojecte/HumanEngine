package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestPluginConfigurationValidationBeforeWriter(t *testing.T) {
	_, main, _, gate := fixtureServer(t)
	authority, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	var seq int
	var name, dbpath string
	if err = main.QueryRow("PRAGMA database_list").Scan(&seq, &name, &dbpath); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	trace := filepath.Join(root, "trace")
	pathJSON, _ := json.Marshal(dbpath)
	traceJSON, _ := json.Marshal(trace)
	source := `import {DatabaseSync} from 'node:sqlite';import {appendFileSync} from 'node:fs';const db=new DatabaseSync(` + string(pathJSON) + `);let held,count=0;const trace=(v)=>appendFileSync(` + string(traceJSON) + `,v+'\n');export default {manifest:{id:'operation',name:'Operation',version:'1',capabilities:[],configSchema:{type:'object',additionalProperties:true}},setup(){},validateConfiguration(c){count++;if(c===held)throw Error('validator_object_reused');db.exec('BEGIN IMMEDIATE');db.exec('ROLLBACK');trace('validate-before-begin');held=c;c.mutated=true},prepareConfiguration(c){if(count!==2)throw Error('operation_module_state_lost');if(c!==held || !c.mutated)throw Error('identity_lost');let locked=false;try{db.exec('BEGIN IMMEDIATE');db.exec('ROLLBACK')}catch(e){locked=String(e).includes('locked')}if(!locked)throw Error('prepare_outside_transaction');trace('prepare-inside-begin');if(c.fail)throw Error('expected_prepare_failure');return c}}`
	if err = os.WriteFile(filepath.Join(root, "operation.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	manager, err := pluginmanager.New(main, authority, host, new(botruntime.Runtime), "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.RegisterV1(context.Background(), "operation.mjs"); err != nil {
		t.Fatal(err)
	}
	config, _ := jsjson.Parse(`{"value":42}`)
	if _, err = manager.Configure(context.Background(), "operation", config.(*jsjson.Object), "trace"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(trace)
	if string(first) != "validate-before-begin\nvalidate-before-begin\nprepare-inside-begin\n" {
		t.Fatal(string(first))
	}
	var before string
	if err = main.QueryRow("SELECT value FROM config WHERE key='plugin:operation'").Scan(&before); err != nil || !strings.Contains(before, `"mutated":true`) {
		t.Fatal(before, err)
	}
	failure, _ := jsjson.Parse(`{"fail":true}`)
	if _, err = manager.Configure(context.Background(), "operation", failure.(*jsjson.Object), "rollback"); err == nil {
		t.Fatal("prepare failure accepted")
	}
	var after string
	if err = main.QueryRow("SELECT value FROM config WHERE key='plugin:operation'").Scan(&after); err != nil || after != before {
		t.Fatal(before, after, err)
	}
	if host.PID() != 0 {
		t.Fatal("disabled operation retained shared Node")
	}
}

func TestPluginInitialValidationCancellationKeepsOtherPlugin(t *testing.T) {
	_, main, _, gate := fixtureServer(t)
	authority, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	root := t.TempDir()
	trace := filepath.Join(root, "entered")
	traceJSON, _ := json.Marshal(trace)
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	manager, err := pluginmanager.New(main, authority, host, new(botruntime.Runtime), "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	for _, id := range []string{"operation", "other"} {
		source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'` + id + `',name:'Fixture',version:'1',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(c){if(c.block){writeFileSync(` + string(traceJSON) + `,'entered');while(true){}}},setup(c){c.registerAdminPage({id:'page',entry:'/plugins/` + id + `/page.js'});c.registerAdminAction('ping',()=>42);c.registerTool({name:'` + id + `.read',effect:'read',inputSchema:{type:'object'},execute:()=>42});c.beforeSend({priority:1,handler:()=>({})})}}`
		if err = os.WriteFile(filepath.Join(root, id+".mjs"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if err = manager.RegisterV1(context.Background(), id+".mjs"); err != nil {
			t.Fatal(err)
		}
		if err = manager.Enable(context.Background(), id, "enable"); err != nil {
			t.Fatal(err)
		}
	}
	pid := host.PID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	block, _ := jsjson.Parse(`{"block":true}`)
	returned := make(chan error, 1)
	go func() { _, e := manager.Configure(ctx, "operation", block.(*jsjson.Object), "cancel"); returned <- e }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
entered:
	for {
		select {
		case e := <-returned:
			t.Fatalf("validation returned early: %v", e)
		case <-timer.C:
			t.Fatal("validation did not enter")
		case <-ticker.C:
			if b, _ := os.ReadFile(trace); string(b) == "entered" {
				break entered
			}
		}
	}
	cancel()
	select {
	case e := <-returned:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("validation isolate not retired")
	}
	if host.PID() != pid {
		t.Fatal("unrelated shared worker lost")
	}
	if _, err = manager.Action(context.Background(), "operation", "ping", new(jsjson.Object)); err == nil {
		t.Fatal("retired action still advertised")
	}
	value, err := manager.Action(context.Background(), "other", "ping", new(jsjson.Object))
	if err != nil || value != float64(42) {
		t.Fatal(value, err)
	}
	items, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := items[0].(*jsjson.Object)
	enabled, _ := first.Get("enabled")
	pages, _ := first.Get("pages")
	if enabled != false || len(pages.([]any)) != 0 {
		t.Fatal("retired plugin still advertised", enabled, pages)
	}
	if err = manager.Disable(context.Background(), "other", "disable"); err != nil || host.PID() != 0 {
		t.Fatal(err, host.PID())
	}
}
