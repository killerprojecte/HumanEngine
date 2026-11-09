package pluginmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

func queueManager(t *testing.T) (*Manager, *plugins.V1Host, string) {
	t.Helper()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if err := os.Mkdir(authorityPath, 0700); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_ = ledger.Close()
	data := filepath.Join(root, "data")
	if err = os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	main, err := sql.Open("sqlite3", filepath.Join(data, "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { main.Close() })
	_, err = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	gate, _ := json.Marshal(recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"})
	if _, err = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(gate)); err != nil {
		t.Fatal(err)
	}
	authority, err := recovery.OpenWriter(context.Background(), authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authority.Close() })
	host := plugins.NewV1Host("node", io.Discard)
	t.Cleanup(func() { host.Close(context.Background()) })
	manager, err := New(main, authority, host, new(botruntime.Runtime), "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(context.Background()) })
	return manager, host, root
}
func TestConfigurationQueueCancellationReapsInspector(t *testing.T) {
	m, host, root := queueManager(t)
	trace := filepath.Join(root, "pid")
	encoded, _ := json.Marshal(trace)
	source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'queued',configSchema:{type:'object'}},setup(){},validateConfiguration(){writeFileSync(` + string(encoded) + `,String(process.pid))}}`
	if err := os.WriteFile(filepath.Join(root, "queued.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterV1(context.Background(), "queued.mjs"); err != nil {
		t.Fatal(err)
	}
	prior := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(prior)
		}
	}()
	m.mu.Lock()
	m.items["queued"].tail = prior
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := m.Configure(ctx, "queued", new(jsjson.Object), "queued"); returned <- err }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var pid int
waiting:
	for {
		select {
		case err := <-returned:
			t.Fatalf("queue operation returned early: %v", err)
		case <-timer.C:
			t.Fatal("first validation/queue admission absent")
		case <-ticker.C:
			bytes, _ := os.ReadFile(trace)
			pid, _ = strconv.Atoi(string(bytes))
			m.mu.Lock()
			queued := m.items["queued"].tail != prior
			m.mu.Unlock()
			if pid != 0 && queued {
				break waiting
			}
		}
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled queued inspector not reaped")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("queued operation retained temporary Node")
	}
	if host.PID() != 0 {
		t.Fatal("disabled operation created shared host")
	}
	m.mu.Lock()
	tail := m.items["queued"].tail
	m.mu.Unlock()
	select {
	case <-tail:
		t.Fatal("cancelled operation let later queue overtake predecessor")
	default:
	}
	close(prior)
	released = true
	select {
	case <-tail:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled queue fence did not drain")
	}
}

func TestConcurrentConfigurationRetirement(t *testing.T) {
	oracleBytes, err := os.ReadFile("testdata/configuration-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Trace  []string
		Action float64
	}
	if err = json.Unmarshal(oracleBytes, &oracle); err != nil {
		t.Fatal(err)
	}

	for _, disable := range []bool{false, true} {
		name := "replace"
		if disable {
			name = "disable"
		}
		t.Run(name, func(t *testing.T) {
			m, host, root := queueManager(t)
			trace := filepath.Join(root, "trace")
			traceJSON, _ := json.Marshal(trace)
			source := `import{appendFileSync}from'node:fs';let count=0,held;export default{manifest:{id:'queued',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(c){held=c;count++;appendFileSync(` + string(traceJSON) + `,'validate:'+count+'\n')},prepareConfiguration(c){if(c!==held)throw Error('identity');appendFileSync(` + string(traceJSON) + `,'prepare:'+count+'\n');return{...c,count}},setup(c){c.registerAdminAction('count',()=>count)}}`
			if err := os.WriteFile(filepath.Join(root, "queued.mjs"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.RegisterV1(context.Background(), "queued.mjs"); err != nil {
				t.Fatal(err)
			}
			if err := m.Enable(context.Background(), "queued", "enable"); err != nil {
				t.Fatal(err)
			}
			prior := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(prior)
				}
			}()
			m.mu.Lock()
			m.items["queued"].tail = prior
			initial := m.items["queued"].revision
			m.mu.Unlock()
			a := make(chan error, 1)
			b := make(chan error, 1)
			waitRevision := func(want uint64, early <-chan error) {
				t.Helper()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case e := <-early:
						t.Fatalf("configuration returned before queue release: %v", e)
					case <-timer.C:
						t.Fatal("queue admission timeout")
					case <-ticker.C:
						m.mu.Lock()
						got := m.items["queued"].revision
						m.mu.Unlock()
						if got == want {
							return
						}
					}
				}
			}
			inputA, _ := jsjson.Parse(`{"who":"a"}`)
			go func() { _, err := m.Configure(context.Background(), "queued", inputA.(*jsjson.Object), "a"); a <- err }()
			waitRevision(initial+1, a)
			inputB, _ := jsjson.Parse(`{"who":"b"}`)
			go func() { _, err := m.Configure(context.Background(), "queued", inputB.(*jsjson.Object), "b"); b <- err }()
			waitRevision(initial+2, b)
			var stopped chan error
			if disable {
				stopped = make(chan error, 1)
				go func() { stopped <- m.Disable(context.Background(), "queued", "disable") }()
				waitRevision(initial+3, stopped)
			}
			close(prior)
			released = true
			for _, done := range []<-chan error{a, b} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("configuration retirement queue deadlocked")
				}
			}
			if disable {
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("disable self-waited on configuration token")
				}
				if host.PID() != 0 {
					t.Fatal("disabled final token retained Node")
				}
			} else {
				value, err := m.Action(context.Background(), "queued", "count", new(jsjson.Object))
				if err != nil || value != oracle.Action {
					t.Fatal(value, err)
				}
			}
			config, err := m.Config(context.Background(), "queued")
			if err != nil {
				t.Fatal(err)
			}
			o := config.(*jsjson.Object)
			if prop(o, "count") != float64(5) || prop(o, "who") != "b" {
				t.Fatal("configuration lost accepted module state", config)
			}
			bytes, _ := os.ReadFile(trace)
			oracleTrace := oracle.Trace
			if disable {
				oracleTrace = oracleTrace[:len(oracleTrace)-1]
			}
			expected := strings.Join(oracleTrace, "\n") + "\n"
			if string(bytes) != expected {
				t.Fatal("module trace", string(bytes), "want", expected)
			}
		})
	}
}

func TestConfigurationDuringAsyncActivationKeepsModule(t *testing.T) {
	oracleBytes, oracleError := os.ReadFile("testdata/async-configuration-source.json")
	if oracleError != nil {
		t.Fatal(oracleError)
	}
	var oracle struct {
		Action float64
		Trace  []string
	}
	if oracleError = json.Unmarshal(oracleBytes, &oracle); oracleError != nil {
		t.Fatal(oracleError)
	}
	m, host, root := queueManager(t)
	entered := filepath.Join(root, "entered")
	release := filepath.Join(root, "release")
	late := filepath.Join(root, "late")
	pending := late + ".pending"
	staged := filepath.Join(root, "staged")
	publish := filepath.Join(root, "publish")
	paths, _ := json.Marshal([]string{entered, release, late, pending, staged, publish})
	source := `import{existsSync,writeFileSync,openSync,writeSync,closeSync,renameSync}from'node:fs';
const[entered,release,late,pending,staged,publish]=` + string(paths) + `;
const waitFor=path=>new Promise(resolve=>{const timer=setInterval(()=>{if(existsSync(path)){clearInterval(timer);resolve()}},1)});
let count=0;
export default{
 manifest:{id:'queued',capabilities:[],configSchema:{type:'object',additionalProperties:true}},
 validateConfiguration(){count++},
 async setup(c){
  if(c.config.who==='a'){
   writeFileSync(entered,'yes');
   await waitFor(release);
   let result;
   try{c.registerAdminAction('stale',()=>0);result='accepted'}catch(e){result=e.message}
   // Hold the create-before-write window open so the observer checks it explicitly.
   const fd=openSync(pending,'w');
   writeFileSync(staged,'yes');
   await waitFor(publish);
   writeSync(fd,result);
   closeSync(fd);
   // The completion path becomes visible only after its full payload is written.
   renameSync(pending,late);
   return;
  }
  c.registerAdminAction('count',()=>count);
 }
}`
	if err := os.WriteFile(filepath.Join(root, "queued.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterV1(context.Background(), "queued.mjs"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background(), "queued", "initial"); err != nil {
		t.Fatal(err)
	}
	pid := host.PID()
	first := make(chan error, 1)
	a, _ := jsjson.Parse(`{"who":"a"}`)
	go func() { _, err := m.Configure(context.Background(), "queued", a.(*jsjson.Object), "a"); first <- err }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
waiting:
	for {
		select {
		case err := <-first:
			t.Fatalf("setup returned before successor: %v", err)
		case <-timer.C:
			t.Fatal("async setup did not enter")
		case <-ticker.C:
			if _, err := os.Stat(entered); err == nil {
				break waiting
			}
		}
	}
	b, _ := jsjson.Parse(`{"who":"b"}`)
	second := make(chan error, 1)
	go func() { _, err := m.Configure(context.Background(), "queued", b.(*jsjson.Object), "b"); second <- err }()
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("superseded setup succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded async setup retained queue")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("successor configuration deadlocked")
	}
	if host.PID() != pid {
		t.Fatal("reconfiguration replaced physical module")
	}
	value, err := m.Action(context.Background(), "queued", "count", new(jsjson.Object))
	if err != nil || value != oracle.Action {
		t.Fatal("lost module counter", value, err)
	}
	if err = os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	// A successful ReadFile does not imply a writer has published its payload.
	// Reproduce the old fixture's empty observation under a gate, not a sleep.
	stagingTimeout := time.NewTimer(5 * time.Second)
	defer stagingTimeout.Stop()
staging:
	for {
		select {
		case <-stagingTimeout.C:
			t.Fatal("late setup did not stage its result")
		case <-ticker.C:
			if _, err := os.Stat(staged); err == nil {
				break staging
			}
		}
	}
	if result, err := os.ReadFile(pending); err != nil || len(result) != 0 {
		t.Fatal("create-before-write window not reproduced", string(result), err)
	}
	if result, err := os.ReadFile(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unpublished result became visible", string(result), err)
	}
	if err = os.WriteFile(publish, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-timeout.C:
			t.Fatal("late setup did not finish")
		case <-ticker.C:
			if result, e := os.ReadFile(late); e == nil {
				if string(result) != "plugin_unloaded" {
					t.Fatal("stale setup registered", string(result))
				}
				if err = m.Disable(context.Background(), "queued", "done"); err != nil {
					t.Fatal(err)
				}
				if host.PID() != 0 || syscall.Kill(pid, 0) == nil {
					t.Fatal("module retained after final disable")
				}
				return
			}
		}
	}
}
