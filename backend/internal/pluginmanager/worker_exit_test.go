package pluginmanager

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnexpectedWorkerExitRevokesOnlyExactRegistrations(t *testing.T) {
	m, host, root := queueManager(t)
	source := `export default{manifest:{id:'exit',capabilities:['model-protocol'],configSchema:{type:'object'}},setup(c){c.registerAdminPage({id:'page',title:'Page',entry:'/plugins/exit/page.js'});c.registerAdminAction('crash',()=>process.exit(0));c.registerAdminAction('ping',()=>42);c.beforeSend({priority:0,handler:segments=>({action:'replace',segments})});c.registerTool({name:'exit.read',effect:'read',inputSchema:{type:'object'},execute:()=>42});c.registerModelProtocol('model','Model',{complete(){return{text:'',toolCalls:[]}},listModels(){return[{id:'actual'}]}})}}`
	if err := os.WriteFile(filepath.Join(root, "exit.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterV1(context.Background(), "exit.mjs"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background(), "exit", "initial"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	item := m.items["exit"]
	oldGeneration := item.hostGeneration
	expected := len(item.cleanups)
	var removed atomic.Int32
	for index, cleanup := range item.cleanups {
		cleanup := cleanup
		item.cleanups[index] = func() { cleanup(); removed.Add(1) }
	}
	m.mu.Unlock()
	if expected != 2 {
		t.Fatal("hook and tool extension registration missing", expected)
	}
	if _, err := m.Action(context.Background(), "exit", "crash", new(jsjson.Object)); err == nil {
		t.Fatal("exiting worker action succeeded")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		cleared := !item.enabled && item.hostGeneration == 0 && len(item.cleanups) == 0 && len(item.pages) == 0 && len(item.actions) == 0
		m.mu.Unlock()
		if cleared && host.PID() == 0 && removed.Load() == int32(expected) {
			break
		}
		select {
		case <-timer.C:
			t.Fatal("unexpected exit retained native capabilities", removed.Load())
		case <-ticker.C:
		}
	}
	list, err := m.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if prop(list[0].(*jsjson.Object), "enabled") != false {
		t.Fatal("list still advertised enabled")
	}
	if err = m.Enable(context.Background(), "exit", "restart"); err != nil {
		t.Fatal(err)
	}
	current, ok := host.RuntimeGeneration("exit")
	if !ok || current == oldGeneration {
		t.Fatal("Enable did not restart worker")
	}
	m.runtimeStopped(plugins.RuntimeStop{ID: "exit", Generation: oldGeneration})
	value, err := m.Action(context.Background(), "exit", "ping", new(jsjson.Object))
	if err != nil || value != float64(42) {
		t.Fatal("old exit removed replacement", value, err)
	}
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 1 {
		t.Fatal(protocols)
	}
	result, err := host.CallModelProtocol(context.Background(), protocols[0].ID, protocols[0].Generation, "listModels", []any{map[string]any{}})
	if err != nil || string(result) != `[{"id":"actual"}]` {
		t.Fatal(string(result), err)
	}
	if err = m.Disable(context.Background(), "exit", "done"); err != nil || host.PID() != 0 {
		t.Fatal(err, host.PID())
	}
}
