package memeservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestAnalysisReleasesActualV1ContinuationOnEveryExit(t *testing.T) {
	for _, mode := range []string{"success", "invalid-json", "invalid-tags", "cancel-final-writer"} {
		t.Run(mode, func(t *testing.T) {
			f, s, codec, c, _, _, asset := nativeImageFixture(t)
			dir := t.TempDir()
			log := filepath.Join(dir, "probe.jsonl")
			arrived := filepath.Join(dir, "arrived")
			resume := filepath.Join(dir, "resume")
			node, e := exec.LookPath("node")
			if e != nil {
				t.Fatal(e)
			}
			probe, e := filepath.Abs("testdata/continuation-probe.mjs")
			if e != nil {
				t.Fatal(e)
			}
			wrapper := filepath.Join(dir, "node-probe")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := "#!/bin/sh\nexec " + quote(node) + " " + quote(probe) + " " + quote(node) + " " + quote(log) + " \"$@\"\n"
			if e = os.WriteFile(wrapper, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			host := plugins.NewV1Host(wrapper, nil)
			t.Cleanup(func() { host.Close(context.Background()) })
			tags := emptyTags()
			set(tags, "selection", fields("usable", true, "shareable", true, "confidence", 0.99, "containsPersonalData", false, "sourceSpecific", false, "containsRealPerson", false))
			text := stringify(tags)
			if mode == "invalid-json" {
				text = "{"
			}
			if mode == "invalid-tags" {
				text = `{"ocr":1}`
			}
			source := `import fs from 'node:fs';export default{manifest:{id:'analysis',configSchema:{type:'object',properties:{text:{type:'string'},arrived:{type:'string'},resume:{type:'string'},wait:{type:'boolean'}}},capabilities:['model-protocol']},setup(ctx){const token={toJSON(){throw Error('opaque must not serialize')}};token.self=token;ctx.registerModelProtocol('vision','Synthetic analysis',{async complete(){fs.writeFileSync(ctx.config.arrived,'ready');if(ctx.config.wait)while(!fs.existsSync(ctx.config.resume))await new Promise(r=>setTimeout(r,1));return{text:ctx.config.text,toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:token}}})}}`
			path := filepath.Join(dir, "analysis.mjs")
			if e = os.WriteFile(path, []byte(source), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = host.Enable(context.Background(), "analysis", path, map[string]any{"text": text, "arrived": arrived, "resume": resume, "wait": mode == "cancel-final-writer"}); e != nil {
				t.Fatal(e)
			}
			zero, timeout := float64(0), float64(30000)
			policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "analysis:vision", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "vision"}}}, Routes: []modelservice.Route{{Role: "vision", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
			if e != nil {
				t.Fatal(e)
			}
			models := &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{Credentials: syntheticCredentials{}, Budgets: providertransport.ExplicitOff{}, Images: providertransport.NativeImageSerializer{Compressor: codec}}, Host: host, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: "analysis:vision", BaseURL: "https://synthetic.invalid"}}, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": "analysis:vision", "baseUrl": "https://synthetic.invalid"}}, ModelSnapshots: map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, e := s.Analyze(ctx, c, str(get(asset, "id")), models, codec, AnalyzeOptions{Selection: &SelectionContext{Persona: "Synthetic", SourceContextKnown: true}})
				result <- e
			}()
			if mode == "cancel-final-writer" {
				waitUntil(t, func() bool { _, e := os.Stat(arrived); return e == nil })
				held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				go func() {
					done <- f.authority.WithWriteSession(context.Background(), f.main, func(*recovery.WriteSession) error { close(held); <-release; return nil })
				}()
				<-held
				before := f.main.Stats().WaitCount
				if e = os.WriteFile(resume, []byte("go"), 0600); e != nil {
					close(release)
					t.Fatal(e)
				}
				waitUntil(t, func() bool { return f.main.Stats().WaitCount > before })
				cancel()
				close(release)
				if e = <-done; e != nil {
					t.Fatal(e)
				}
			}
			select {
			case e = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("analysis did not settle")
			}
			if mode == "success" && e != nil {
				t.Fatal(e)
			}
			if mode != "success" && e == nil {
				t.Fatal("analysis failure was lost")
			}
			if mode == "cancel-final-writer" && e.Error() != "analysis_cancelled" {
				t.Fatalf("original cancellation changed: %v", e)
			}
			var rows []struct {
				Phase  string
				Result struct{ Pending, Claimed int }
				Failed bool
			}
			waitUntil(t, func() bool {
				raw, err := os.ReadFile(log)
				if err != nil {
					return false
				}
				rows = nil
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					var row struct {
						Phase  string
						Result struct{ Pending, Claimed int }
						Failed bool
					}
					if json.Unmarshal([]byte(line), &row) != nil {
						return false
					}
					rows = append(rows, row)
				}
				return len(rows) >= 2
			})
			if len(rows) != 2 || rows[0].Phase != "claim_continuation" || rows[0].Result.Claimed != 1 || rows[0].Result.Pending != 0 || rows[0].Failed || rows[1].Phase != "release_continuation" || rows[1].Result.Claimed != 0 || rows[1].Result.Pending != 0 || rows[1].Failed {
				t.Fatalf("actual worker continuation ownership not released: %s", strconv.Quote(func() string { b, _ := os.ReadFile(log); return string(b) }()))
			}
		})
	}
}
