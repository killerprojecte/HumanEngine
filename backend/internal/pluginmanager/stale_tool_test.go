package pluginmanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestRetainedToolExecuteCannotEnterReplacementWorker(t *testing.T) {
	m, host, root := queueManager(t)
	source := `export default{manifest:{id:'retained',configSchema:{type:'object'}},setup(c){let calls=0;const handle=c.registerTool({name:'retained.read',effect:'read',inputSchema:{type:'object'},execute(){return ++calls}});c.registerAdminAction('inspect',()=>({handle,calls}));c.registerAdminAction('crash',()=>process.exit(0))}}`
	if err := os.WriteFile(filepath.Join(root, "retained.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterV1(context.Background(), "retained.mjs"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background(), "retained", "first"); err != nil {
		t.Fatal(err)
	}
	value, err := m.Action(context.Background(), "retained", "inspect", new(jsjson.Object))
	if err != nil {
		t.Fatal(err)
	}
	handle := text(value.(*jsjson.Object), "handle")
	tool, _ := jsjson.NewObject(jsjson.Member{Key: "handle", Value: handle}, jsjson.Member{Key: "effect", Value: "read"})
	m.mu.Lock()
	item := m.items["retained"]
	oldGeneration, revision, life := item.hostGeneration, item.revision, item.ctx
	m.mu.Unlock()
	// This is the very closure installed on Extension.Execute, retained by a
	// previously prepared turn. Its invocation is held until after replacement.
	oldExecute := m.toolExecutor("retained", item, revision, life, oldGeneration, map[string]*jsjson.Object{"retained.read": tool})
	if _, err = m.Action(context.Background(), "retained", "crash", new(jsjson.Object)); err == nil {
		t.Fatal("crash succeeded")
	}
	deadline := time.After(5 * time.Second)
	for host.PID() != 0 || life.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("worker did not exit")
		case <-time.After(time.Millisecond):
		}
	}
	if err = m.Enable(context.Background(), "retained", "replacement"); err != nil {
		t.Fatal(err)
	}
	generation, ok := host.RuntimeGeneration("retained")
	if !ok || generation == oldGeneration {
		t.Fatal("missing new activation")
	}
	settled := make(chan error, 1)
	oldExecute(context.Background(), "retained.read", new(jsjson.Object), &botruntime.TurnContext{}, func(_ any, err error) { settled <- err })
	select {
	case err = <-settled:
		if err == nil {
			t.Fatal("old Execute succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old Execute did not settle")
	}
	value, err = m.Action(context.Background(), "retained", "inspect", new(jsjson.Object))
	if err != nil || prop(value.(*jsjson.Object), "calls") != float64(0) {
		t.Fatal("old tool ran in replacement", value, err)
	}
	m.mu.Lock()
	newLife := item.ctx
	m.mu.Unlock()
	newExecute := m.toolExecutor("retained", item, revision, newLife, generation, map[string]*jsjson.Object{"retained.read": tool})
	newExecute(context.Background(), "retained.read", new(jsjson.Object), &botruntime.TurnContext{}, func(_ any, err error) { settled <- err })
	select {
	case err = <-settled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new Execute did not settle")
	}
	value, err = m.Action(context.Background(), "retained", "inspect", new(jsjson.Object))
	if err != nil || prop(value.(*jsjson.Object), "calls") != float64(1) {
		t.Fatal("new tool not usable", value, err)
	}
	if err = m.Disable(context.Background(), "retained", "done"); err != nil || host.PID() != 0 {
		t.Fatal(err, host.PID())
	}
}
