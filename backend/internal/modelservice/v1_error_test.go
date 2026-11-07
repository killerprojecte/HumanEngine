package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestV1SDKIdentityControlsActualAttemptPolicy(t *testing.T) {
	root := t.TempDir()
	sdk := (&url.URL{Scheme: "file", Path: filepath.Join(root, "sdk", "model-protocols.ts")}).String()
	host, err := plugins.NewV1HostWithOptions("node", io.Discard, plugins.V1HostOptions{ModelProtocolModuleURLs: []string{sdk}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	path := filepath.Join(root, "plugin.mjs")
	source := `import {ModelProtocolError} from './sdk/model-protocols.ts';
 export default {manifest:{id:'fixture',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Synthetic',{
 complete(context,mode){
 if(mode==='typed')throw new ModelProtocolError('http',{status:503,retryAfterMs:751});
 if(mode==='timeout')throw new ModelProtocolError('timeout');
 if(mode==='configuration')throw new ModelProtocolError('configuration');
 if(mode==='admission'){const e=new ModelProtocolError('network');e.message='memory_read_disabled';throw e}
 if(mode==='shape')throw {name:'ModelProtocolError',kind:'http',status:503,retryable:true};
 if(mode==='cause')throw Error('synthetic-private-detail',{cause:new ModelProtocolError('network')});
 throw Error('provider_http_503');
 }})}}`
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enable(context.Background(), "fixture", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	for _, test := range []struct {
		mode                  string
		attempts              int
		retryable, classified bool
		code                  string
	}{
		{"typed", 3, true, true, "provider_http_503"},
		{"timeout", 1, false, true, "provider_timeout"},
		{"configuration", 1, false, true, "protocol_configuration_error"},
		{"admission", 1, false, true, "memory_read_disabled"},
		{"shape", 1, false, false, ""}, {"cause", 1, false, false, ""}, {"name", 1, false, false, ""},
	} {
		t.Run(test.mode, func(t *testing.T) {
			cfg := baseConfig("fixture:custom", 10000)
			cfg.Providers[0].RetryCount = n(2)
			var calls atomic.Int32
			var delays []float64
			policy, err := New(cfg, Options{Scheduler: scheduler.NewModelScheduler(), Sleep: func(ctx context.Context, delay float64) error { delays = append(delays, delay); return ctx.Err() }})
			if err != nil {
				t.Fatal(err)
			}
			_, failure := policy.Run(context.Background(), Request{Role: "replyer"}, func(ctx context.Context, a Attempt, settle func(any, error)) {
				go func() {
					calls.Add(1)
					result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, plugins.ModelCompletionRequest{ModelID: a.Candidate.Model.ID, Context: map[string]any{}, Messages: test.mode, Validate: func(json.RawMessage) error { return errors.New("unexpected success") }})
					if result != nil {
						_ = result.Continuation().Release()
					}
					classified, ok := V1ProtocolFailure(err)
					if ok != test.classified {
						t.Error("unexpected classification", ok, err)
					}
					if ok {
						if classified.Code != test.code {
							t.Error("unexpected source code", classified.Code, test.code)
						}
						if _, ok := V1ProtocolFailure(fmt.Errorf("wrapped: %w", err)); ok {
							t.Error("wrapper granted retry")
						}
						public, _ := json.Marshal(classified)
						if strings.Contains(string(public), "synthetic-private-detail") {
							t.Error("cause exposed", string(public))
						}
						settle(nil, classified)
					} else {
						settle(nil, err)
					}
				}()
			})
			if int(calls.Load()) != test.attempts {
				t.Fatal("wrong retries", calls.Load(), test.attempts)
			}
			modelErr, ok := failure.(*ModelError)
			if !ok || modelErr.Retryable != test.retryable {
				t.Fatal("wrong policy result", failure)
			}
			if len(delays) != test.attempts-1 {
				t.Fatal("wrong retry wait count", delays)
			}
			for _, delay := range delays {
				if delay != 751 {
					t.Fatal("private Retry-After was lost", delay)
				}
			}
		})
	}
	for _, err := range []error{errors.New("provider_http_503"), NewError("provider_http_503", nil, true), &plugins.ProtocolInvocationError{}} {
		if _, ok := V1ProtocolFailure(err); ok {
			t.Fatal("arbitrary Go error reconstructed", err)
		}
	}
}
