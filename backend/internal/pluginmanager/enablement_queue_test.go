package pluginmanager

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnableConfigurationPromotionMatchesSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/enablement-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var sourceCases []struct {
		Ordering string
		Trace    []string
		Action   float64
		Outcomes []struct{ Status, Code string }
	}
	if err = json.Unmarshal(raw, &sourceCases); err != nil {
		t.Fatal(err)
	}
	for _, sourceCase := range sourceCases {
		t.Run(sourceCase.Ordering, func(t *testing.T) {
			m, host, root := queueManager(t)
			tracePath := filepath.Join(root, "trace")
			quoted, _ := json.Marshal(tracePath)
			source := `import{appendFileSync}from'node:fs';let count=0,held;const trace=s=>appendFileSync(` + string(quoted) + `,s+'\n');export default{manifest:{id:'queued',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(c){held=c;trace('validate:'+ ++count)},prepareConfiguration(c){if(c!==held)throw Error('identity');trace('prepare:'+count);return{...c,count}},setup(c){trace('setup:'+count);c.registerAdminAction('count',()=>count)}}`
			if err = os.WriteFile(filepath.Join(root, "queued.mjs"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if err = m.RegisterV1(context.Background(), "queued.mjs"); err != nil {
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
			item := m.items["queued"]
			item.tail = prior
			initial := item.revision
			m.mu.Unlock()
			enabled, configured := make(chan error, 1), make(chan error, 1)
			enable := func() { go func() { enabled <- m.Enable(context.Background(), "queued", "enable") }() }
			configure := func() {
				go func() {
					v, _ := jsjson.Parse(`{"who":"configured"}`)
					_, e := m.Configure(context.Background(), "queued", v.(*jsjson.Object), "configure")
					configured <- e
				}()
			}
			wait := func(ready func() bool, early <-chan error) {
				t.Helper()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case e := <-early:
						t.Fatalf("operation returned before queue release: %v", e)
					case <-timer.C:
						t.Fatal("admission timeout")
					case <-ticker.C:
						if ready() {
							return
						}
					}
				}
			}
			enabledReady := func() bool { m.mu.Lock(); defer m.mu.Unlock(); return item.desired }
			configuredReady := func() bool { m.mu.Lock(); defer m.mu.Unlock(); return item.revision == initial+1 }
			if sourceCase.Ordering == "enable-then-configure" {
				enable()
				wait(enabledReady, enabled)
				configure()
				wait(configuredReady, configured)
			} else {
				configure()
				wait(configuredReady, configured)
				enable()
				wait(enabledReady, enabled)
			}
			pid := host.PID()
			if pid == 0 {
				t.Fatal("explicit configuration process absent")
			}
			close(prior)
			released = true
			for index, ch := range []<-chan error{enabled, configured} {
				select {
				case e := <-ch:
					expected := sourceCase.Outcomes[index]
					if (e == nil) != (expected.Status == "fulfilled") {
						t.Fatal("outcome mismatch", index, e, expected)
					}
					if e != nil && !strings.Contains(e.Error(), expected.Code) {
						t.Fatal(e, expected)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("promotion deadlocked")
				}
			}
			value, e := m.Action(context.Background(), "queued", "count", new(jsjson.Object))
			if e != nil || value != sourceCase.Action {
				t.Fatal(value, e)
			}
			if host.PID() != pid {
				t.Fatal("promoted module moved to different process")
			}
			trace, e := os.ReadFile(tracePath)
			if e != nil || string(trace) != strings.Join(sourceCase.Trace, "\n")+"\n" {
				t.Fatal(string(trace), e)
			}
			if e = m.Disable(context.Background(), "queued", "disable"); e != nil {
				t.Fatal(e)
			}
			if host.PID() != 0 || syscall.Kill(pid, 0) == nil {
				t.Fatal("final disable did not physically join promoted process")
			}
		})
	}
}

func TestTemporaryConfigurationOwnersShareAndDrain(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "temporary_only"
		if existing {
			name = "existing_runtime"
		}
		t.Run(name, func(t *testing.T) {
			m, host, root := queueManager(t)
			for _, id := range []string{"first", "second", "other"} {
				source := `export default{manifest:{id:'` + id + `',capabilities:[],configSchema:{type:'object'}},setup(c){c.registerAdminAction('ping',()=>42)}}`
				if err := os.WriteFile(filepath.Join(root, id+".mjs"), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				if err := m.RegisterV1(context.Background(), id+".mjs"); err != nil {
					t.Fatal(err)
				}
			}
			if existing {
				if err := m.Enable(context.Background(), "other", "other"); err != nil {
					t.Fatal(err)
				}
			}
			prior := make(chan struct{})
			defer close(prior)
			m.mu.Lock()
			m.items["first"].tail = prior
			m.items["second"].tail = prior
			m.mu.Unlock()
			var cancels []context.CancelFunc
			var results []chan error
			pid := host.PID()
			for _, id := range []string{"first", "second"} {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cancels = append(cancels, cancel)
				result := make(chan error, 1)
				results = append(results, result)
				go func(id string) { _, e := m.Configure(ctx, id, new(jsjson.Object), id); result <- e }(id)
				timer := time.NewTimer(5 * time.Second)
				ticker := time.NewTicker(time.Millisecond)
				admitted := false
				for !admitted {
					select {
					case e := <-result:
						t.Fatalf("returned before queue release: %v", e)
					case <-timer.C:
						t.Fatal("configuration admission timeout")
					case <-ticker.C:
						m.mu.Lock()
						admitted = m.items[id].revision == 1
						m.mu.Unlock()
					}
				}
				timer.Stop()
				ticker.Stop()
				current := host.PID()
				if current == 0 {
					t.Fatal("explicit operation missing process")
				}
				if pid != 0 && current != pid {
					t.Fatal("second control process created")
				}
				pid = current
			}
			for index, cancel := range cancels {
				cancel()
				select {
				case e := <-results[index]:
					if e == nil {
						t.Fatal("cancelled configuration succeeded")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled module not drained")
				}
				if index == 0 && host.PID() != pid {
					t.Fatal("first cancellation killed second module")
				}
			}
			if existing {
				value, e := m.Action(context.Background(), "other", "ping", new(jsjson.Object))
				if e != nil || value != float64(42) || host.PID() != pid {
					t.Fatal(value, e, host.PID())
				}
				if e = m.Disable(context.Background(), "other", "done"); e != nil {
					t.Fatal(e)
				}
			}
			if host.PID() != 0 || syscall.Kill(pid, 0) == nil {
				t.Fatal("zero activity retained Node")
			}
		})
	}
}
