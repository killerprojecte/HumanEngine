package plugins

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestV1LazySharedHostAndActualUnload(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	if host.PID() != 0 {
		t.Fatal("Node started without V1")
	}
	root := t.TempDir()
	write := func(id string) string {
		path := filepath.Join(root, id+".mjs")
		source := `export default {manifest:{id:"` + id + `",name:"Fixture",version:"1",configSchema:{},capabilities:[]},setup(ctx){ctx.registerAdminAction('echo', async input=>({echo:input}));ctx.onDispose(()=>{});}}`
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first, err := host.Enable(ctx, "one", write("one"), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pid := host.PID()
	if pid == 0 {
		t.Fatal("host not started")
	}
	if _, err = host.Enable(ctx, "two", write("two"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if host.PID() != pid {
		t.Fatal("not a shared process")
	}
	var loaded struct{ Registrations []struct{ Handle string } }
	if err = json.Unmarshal(first, &loaded); err != nil {
		t.Fatal(err)
	}
	result, err := host.Call(ctx, "one", loaded.Registrations[0].Handle, []any{"value"})
	if err != nil || string(result) != `{"echo":"value"}` {
		t.Fatal(string(result), err)
	}
	if err = host.Disable(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if host.PID() != pid {
		t.Fatal("remaining plugin lost host")
	}
	if _, err = host.Call(ctx, "one", "1", nil); err != ErrNotLoaded {
		t.Fatal("disabled callback accepted")
	}
	if err = host.Disable(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if host.PID() != 0 {
		t.Fatal("last disable did not exit Node")
	}
	if _, err = host.Enable(ctx, "one", write("one"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if host.PID() == pid {
		t.Fatal("old process reused after unload")
	}
}

func TestV1DisableInterruptsSetupAndUnloadsIsolate(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "waiting.mjs")
	logPath := path + ".cleanup"
	readyPath := path + ".ready"
	encodedLog, _ := json.Marshal(logPath)
	encodedReady, _ := json.Marshal(readyPath)
	source := `import {appendFileSync,writeFileSync} from 'node:fs'; export default {manifest:{id:'waiting'},async setup(ctx){ctx.onDispose(()=>appendFileSync(` + string(encodedLog) + `,'once\n'));writeFileSync(` + string(encodedReady) + `,'ready');await new Promise(resolve=>ctx.signal.addEventListener('abort',resolve,{once:true}));}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := host.Enable(ctx, "waiting", path, map[string]any{}); finished <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup did not begin")
		}
		runtime.Gosched()
	}
	if err := host.Disable(ctx, "waiting"); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil {
		t.Fatal("interrupted setup reported loaded")
	}
	if host.PID() != 0 {
		t.Fatal("setup left Node alive")
	}
	bytes, err := os.ReadFile(logPath)
	if err != nil || string(bytes) != "once\n" {
		t.Fatal("aborted setup cleanup:", string(bytes), err)
	}
}

func TestV1StoredConfigurationAndDisposeExactlyOnce(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "config.mjs")
	logPath, _ := json.Marshal(filepath.Join(root, "cleanup.txt"))
	source := `import {appendFileSync} from 'node:fs'; export default {manifest:{id:'config'},prepareConfiguration(){throw Error('prepare is a write operation, not startup')},validateConfiguration(config){return {...config, normalized:true}},setup(ctx){if(ctx.config.normalized)throw Error('startup replaced stored configuration');ctx.onDispose(()=>appendFileSync(` + string(logPath) + `,'once\n'));ctx.registerAdminAction('config',async()=>ctx.config)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "config", path, map[string]any{"stored": true}); err != nil {
		t.Fatal(err)
	}
	if err := host.Disable(ctx, "config"); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, "cleanup.txt"))
	if err != nil || string(bytes) != "once\n" {
		t.Fatal(string(bytes), err)
	}
}

func TestV1CancellationTerminatesStuckIsolateWithoutStoppingOtherPlugin(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	for _, id := range []string{"keeper", "stuck"} {
		path := filepath.Join(root, id+".mjs")
		source := `export default {manifest:{id:'` + id + `'},setup(ctx){ctx.registerAdminAction('echo',async()=>true);`
		if id == "stuck" {
			source += `ctx.onDispose(()=>new Promise(()=>{}));`
		}
		source += `}}`
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := host.Enable(ctx, id, path, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	pid := host.PID()
	cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := host.Disable(cancelCtx, "stuck"); err == nil {
		t.Fatal("missing cancellation result")
	}
	if host.PID() != pid {
		t.Fatal("other V1 plugin lost shared process")
	}
	if result, err := host.Call(ctx, "keeper", "1", nil); err != nil || string(result) != "true" {
		t.Fatal(string(result), err)
	}
}

func TestV1ExitedWorkerDoesNotHangDisable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	path := filepath.Join(t.TempDir(), "exit.mjs")
	source := `export default {manifest:{id:'exit'},setup(ctx){ctx.registerAdminAction('exit',async()=>process.exit(0))}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "exit", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Call(ctx, "exit", "1", nil); err == nil {
		t.Fatal("exit returned success")
	}
	if _, err := host.Call(ctx, "exit", "1", nil); err == nil {
		t.Fatal("dead worker returned success")
	}
	_ = host.Disable(ctx, "exit")
	if ctx.Err() != nil {
		t.Fatal("dead worker hung until context expiration")
	}
	if host.PID() != 0 {
		t.Fatal("dead worker kept shared Node alive")
	}
}

func TestV1CancelledBusyStartupTerminatesIsolate(t *testing.T) {
	host := NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	root := t.TempDir()
	path := filepath.Join(root, "busy.mjs")
	source := `export default {manifest:{id:'busy'},setup(){while(true){}}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := host.Enable(ctx, "busy", path, map[string]any{}); err == nil {
		t.Fatal("cancelled setup succeeded")
	}
	if host.PID() != 0 {
		t.Fatal("cancelled busy setup retained Node")
	}
}

func TestV1TeardownBarrierAndReenable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "barrier.mjs")
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	encodedStarted, _ := json.Marshal(started)
	encodedRelease, _ := json.Marshal(release)
	source := `import {writeFileSync,existsSync} from 'node:fs';export default {manifest:{id:'barrier'},setup(ctx){ctx.onDispose(async()=>{writeFileSync(` + string(encodedStarted) + `,'started');while(!existsSync(` + string(encodedRelease) + `))await new Promise(resolve=>setTimeout(resolve,1))})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "barrier", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- host.Disable(ctx, "barrier") }()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		runtime.Gosched()
	}
	second := make(chan error, 1)
	enabled := make(chan error, 1)
	host.lifecycle.Lock()
	retiring := host.enabled["barrier"]
	host.lifecycle.Unlock()
	go func() { second <- host.disableLease(ctx, "barrier", retiring) }()
	go func() { _, err := host.Enable(ctx, "barrier", path, map[string]any{}); enabled <- err }()
	select {
	case err := <-first:
		t.Fatal("disable returned before disposer", err)
	default:
	}
	select {
	case err := <-enabled:
		t.Fatal("re-enabled during teardown", err)
	default:
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, result := range []chan error{first, second, enabled} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if host.PID() == 0 {
		t.Fatal("new generation not running")
	}
}

func TestV1PerCallCancellationAndTurnContext(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "turn.mjs")
	started := filepath.Join(root, "started")
	aborted := filepath.Join(root, "aborted")
	encodedStarted, _ := json.Marshal(started)
	encodedAborted, _ := json.Marshal(aborted)
	source := `import {writeFileSync} from 'node:fs';export default {manifest:{id:'turn'},setup(ctx){ctx.registerTool({name:'turn.wait',execute:async(input,context)=>{if(!(context.dependencies instanceof Set)||!context.dependencies.has('source')||!Object.isFrozen(context.scene)||context.validations.length!==0)throw Error('invalid_context');if(input.fast)return 'fast';writeFileSync(` + string(encodedStarted) + `,'started');await new Promise(resolve=>context.signal.addEventListener('abort',resolve,{once:true}));writeFileSync(` + string(encodedAborted) + `,'aborted');return 'must not publish'}});ctx.registerAdminAction('echo',async()=>true)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "turn", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		_, err := host.Call(callCtx, "turn", "1", []any{map[string]any{}, map[string]any{"scene": map[string]any{"id": "scene"}, "dependencies": []string{"source"}}})
		finished <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		runtime.Gosched()
	}
	fast, fastErr := host.Call(ctx, "turn", "1", []any{map[string]any{"fast": true}, map[string]any{"scene": map[string]any{"id": "scene"}, "dependencies": []string{"source"}}})
	if fastErr != nil || string(fast) != `"fast"` {
		t.Fatal("concurrent call was not independent", string(fast), fastErr)
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("cancelled callback succeeded")
	}
	for {
		if _, err := os.Stat(aborted); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		runtime.Gosched()
	}
	if result, err := host.Call(ctx, "turn", "2", nil); err != nil || string(result) != "true" {
		t.Fatal("cancellation ended unrelated calls", string(result), err)
	}
	_ = os.Remove(started)
	if _, err := host.Call(callCtx, "turn", "1", []any{map[string]any{}, map[string]any{}}); err == nil {
		t.Fatal("pre-cancelled call admitted")
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("pre-cancelled call executed guest code", err)
	}
}

func TestV1CallbackReceiversAndSignalLifetimes(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "receivers.mjs")
	source := `let toolSignal,hookSignal;export default{manifest:{id:'receivers'},setup(ctx){ctx.registerTool({name:'receivers.tool',execute:async function(_,context){if(this.name!=='receivers.tool')throw Error('tool receiver');toolSignal=context.signal;return true}});ctx.beforeSend({priority:17,kind:'action',handle:'untrusted-overwrite',handler:async function(segments,context){if(this.priority!==17)throw Error('hook receiver');hookSignal=context.signal;return {action:'continue',segments}}});ctx.registerAdminAction('status',async function(){if(this!==undefined)throw Error('action receiver');return{tool:toolSignal?.aborted,hook:hookSignal?.aborted}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "receivers", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Call(ctx, "receivers", "1", []any{map[string]any{}, map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	turn, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	if _, err := host.Call(turn, "receivers", "2", []any{[]any{}, map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	result, err := host.Call(ctx, "receivers", "3", nil)
	if err != nil || string(result) != `{"tool":true,"hook":false}` {
		t.Fatal(string(result), err)
	}
	cancelTurn()
	for {
		result, err = host.Call(ctx, "receivers", "3", nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(result) == `{"tool":true,"hook":true}` {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	host.lifecycle.Lock()
	retained := len(host.enabled["receivers"].turnStops)
	host.lifecycle.Unlock()
	if retained != 0 {
		t.Fatal("completed turn retained cancellation listeners")
	}
	if _, err := host.Call(context.Background(), "receivers", "2", []any{[]any{}, map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	host.lifecycle.Lock()
	retained = len(host.enabled["receivers"].turnStops)
	host.lifecycle.Unlock()
	if retained != 0 {
		t.Fatal("noncancellable hook retained a turn listener")
	}

}

func TestV1CancelledUncooperativeCallRetiresRPC(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "ignore.mjs")
	started := filepath.Join(root, "started")
	encoded, _ := json.Marshal(started)
	source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'ignore'},setup(ctx){ctx.registerTool({name:'ignore.wait',execute:async()=>{writeFileSync(` + string(encoded) + `,'ready');return new Promise(()=>{})}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "ignore", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		_ = os.Remove(started)
		call, cancel := context.WithCancel(ctx)
		result := make(chan error, 1)
		go func() {
			_, err := host.Call(call, "ignore", "1", []any{map[string]any{}, map[string]any{}})
			result <- err
		}()
		for {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			runtime.Gosched()
		}
		cancel()
		if err := <-result; err == nil {
			t.Fatal("uncooperative cancelled call succeeded")
		}
	}
	host.lifecycle.Lock()
	process := host.process
	host.lifecycle.Unlock()
	result, err := process.call(ctx, map[string]any{"op": "status"})
	var status struct{ Workers, PendingRequests int }
	_ = json.Unmarshal(result, &status)
	if err != nil || status.Workers != 1 || status.PendingRequests != 0 {
		t.Fatal("cancelled RPC retained listeners", string(result), err)
	}
}

type observedErrContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *observedErrContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}
func TestV1CancellationWhileWaitingForAdmission(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "admission.mjs")
	source := `let count=0;export default{manifest:{id:'admission'},setup(ctx){ctx.registerAdminAction('increment',async()=>++count);ctx.registerAdminAction('status',async()=>count)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "admission", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := &observedErrContext{Context: caller, checked: make(chan struct{})}
	host.lifecycle.Lock()
	result := make(chan error, 1)
	go func() { _, err := host.Call(observed, "admission", "1", nil); result <- err }()
	<-observed.checked
	cancel()
	host.lifecycle.Unlock()
	if err := <-result; err == nil {
		t.Fatal("cancelled admission succeeded")
	}
	value, err := host.Call(ctx, "admission", "2", nil)
	if err != nil || string(value) != "0" {
		t.Fatal("cancelled admission ran callback", string(value), err)
	}
}
