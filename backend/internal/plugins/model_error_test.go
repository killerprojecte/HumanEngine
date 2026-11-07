package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func identityFixture(t *testing.T) (*V1Host, ModelProtocolMetadata) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"v1-error-cases.mjs", "v1-error-plugin.mjs"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "foreign"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "foreign", "model-protocols.mjs"), []byte(nodeModelProtocolSDK), 0600); err != nil {
		t.Fatal(err)
	}
	urls := []string{(&url.URL{Scheme: "file", Path: filepath.Join(root, "sdk", "model-protocols.ts")}).String(), (&url.URL{Scheme: "file", Path: filepath.Join(root, "alias", "model-protocols.js")}).String()}
	host, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{ModelProtocolModuleURLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	urls[0] = "file:///mutated-caller-input.ts"
	if host.PID() != 0 {
		t.Fatal("options started Node before any enabled V1 plugin")
	}
	t.Cleanup(func() {
		if err := host.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Enable(context.Background(), "identity", filepath.Join(root, "v1-error-plugin.mjs"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return host, host.RegisteredModelProtocols()[0]
}

func TestV1ProtocolErrorSourceOracle(t *testing.T) {
	var oracle struct {
		Sources map[string]string
		Cases   []struct {
			Reads                 []string
			Mode                  string
			Classified, Retryable bool
			Code                  string
			Status                json.RawMessage
			Delay                 float64
		}
	}
	data, err := os.ReadFile("testdata/v1-model-errors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	for source, hash := range oracle.Sources {
		value, err := os.ReadFile(filepath.Join("../../..", source))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(value)
		if hex.EncodeToString(digest[:]) != hash {
			t.Fatalf("changed source oracle: %s", source)
		}
	}
	host, protocol := identityFixture(t)
	for _, row := range oracle.Cases {
		for _, operation := range []string{"complete", "embeddings", "listModels"} {
			t.Run(operation+"/"+row.Mode, func(t *testing.T) {
				result, err := callProtocolFixture(host, context.Background(), protocol.ID, protocol.Generation, operation, []any{map[string]any{"mode": row.Mode}, row.Mode})
				if result != nil || err == nil || err.Error() != "model_protocol_failure" {
					t.Fatalf("unsanitized failure: %s %v", result, err)
				}
				classification, ok := ClassifyProtocolFailure(err)
				rawReads, readErr := host.Call(context.Background(), "identity", "1", nil)
				var reads []string
				if readErr != nil || json.Unmarshal(rawReads, &reads) != nil || !reflect.DeepEqual(reads, row.Reads) {
					t.Fatalf("getter reads differ: got %s, want %v (%v)", rawReads, row.Reads, readErr)
				}
				if _, ok := ClassifyProtocolFailure(forgedProtocolAs{err}); ok {
					t.Fatal("errors.As minted identity")
				}
				expectedIdentity := row.Classified
				switch row.Mode {
				case "foreign", "shape", "named", "wrapped":
					expectedIdentity = false
				}
				if ok != expectedIdentity {
					t.Fatalf("unexpected SDK identity: got %v, want %v (%+v)", ok, expectedIdentity, classification)
				}
				if classification.Retryable != row.Retryable {
					t.Fatalf("retry differs from Router: %+v, expected %+v", classification, row)
				}
				if ok {
					if row.Mode == "mutated-status-string" {
						if classification.Code != "provider_request_failed" || classification.Status != nil {
							t.Fatal("out-of-contract status was not sanitized", classification)
						}
					} else {
						if classification.Code != row.Code {
							t.Fatal("code differs from Router", classification, row)
						}
						actual, _ := json.Marshal(classification.Status)
						expected := string(row.Status)
						if expected == "" {
							expected = "null"
						}
						if string(actual) != expected {
							t.Fatal("status differs from Router", string(actual), expected)
						}
					}
					delay := 250.0
					if n := classification.RetryAfterMS; n != nil && !math.IsNaN(*n) {
						delay = math.Max(delay, math.Min(10000, math.Max(0, *n)))
					}
					if delay != row.Delay {
						t.Fatal("retry delay differs from Router", delay, row.Delay)
					}
					// Callers may mutate the projection, never the underlying private error.
					classification.Code = "forged"
					if classification.Status != nil {
						*classification.Status = 111
					}
					second, _ := ClassifyProtocolFailure(err)
					if second.Code == "forged" || second.Status != nil && *second.Status == 111 {
						t.Fatal("mutable classification alias")
					}
				}
				public, _ := json.Marshal(err)
				if strings.Contains(string(public), "synthetic-secret") || strings.Contains(string(public), "retryable") || strings.Contains(fmt.Sprintf("%+v", err), "synthetic-secret") {
					t.Fatal("private classification exposed", string(public))
				}
				if _, ok := ClassifyProtocolFailure(fmt.Errorf("wrapped: %w", err)); ok {
					t.Fatal("wrapped identity granted retry")
				}
			})
		}
	}
}

func TestV1ProtocolIdentityBoundary(t *testing.T) {
	host, protocol := identityFixture(t)
	for _, mode := range []string{"foreign-sdk", "visible-getter", "continuation-getter", "thenable-getter"} {
		_, err := callProtocolFixture(host, context.Background(), protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, mode})
		classification, ok := ClassifyProtocolFailure(err)
		if (ok && classification.Retryable) != (mode == "thenable-getter") {
			t.Fatal("incorrect adapter execution boundary", mode, classification, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, "wait-cancellation"})
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatal("SDK failure superseded cancellation", err)
	}
	if err := host.Disable(context.Background(), "identity"); err != nil {
		t.Fatal(err)
	}
	if host.PID() != 0 {
		t.Fatal("last plugin did not stop Node")
	}
	if _, err := callProtocolFixture(host, context.Background(), protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, "network"}); err != ErrNotLoaded {
		t.Fatal("retired generation retained error identity", err)
	}
}

type forgedProtocolAs struct{ target error }

func (e forgedProtocolAs) Error() string { return "ModelProtocolError" }
func (e forgedProtocolAs) As(target any) bool {
	if p, ok := target.(**ProtocolInvocationError); ok {
		*p = e.target.(*ProtocolInvocationError)
		return true
	}
	return false
}
func TestV1ProtocolClassificationCannotBeMinted(t *testing.T) {
	for _, err := range []error{nil, &ProtocolInvocationError{}, (*ProtocolInvocationError)(nil), errors.New("provider_http_503"), forgedProtocolAs{&ProtocolInvocationError{}}} {
		if _, ok := ClassifyProtocolFailure(err); ok {
			t.Fatal("nontransport identity accepted", err)
		}
	}
	var decoded ProtocolInvocationError
	if err := json.Unmarshal([]byte(`{"kind":"http","status":503,"retryable":true,"protocol":{"code":"provider_http_503","retryable":true}}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClassifyProtocolFailure(&decoded); ok {
		t.Fatal("JSON minted identity")
	}
	if _, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{ModelProtocolModuleURLs: []string{"../model-protocols.ts"}}); err == nil {
		t.Fatal("relative deployment alias accepted")
	}
}

func TestV1ActualSDKModuleURLSharesCanonicalConstructor(t *testing.T) {
	path, err := filepath.Abs("../../../src/model-protocols.ts")
	if err != nil {
		t.Fatal(err)
	}
	sdkURL := (&url.URL{Scheme: "file", Path: path}).String()
	sdkJSON, _ := json.Marshal(sdkURL)
	host, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{ModelProtocolModuleURLs: []string{sdkURL}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	plugin := filepath.Join(t.TempDir(), "actual-sdk.mjs")
	source := `import {ModelProtocolError,ModelProtocolRegistry,isBuiltinProtocol} from ` + string(sdkJSON) + `;
 import {ModelProtocolError as CanonicalError} from 'humanengine:v1-model-protocols';
 if(ModelProtocolError!==CanonicalError || typeof ModelProtocolRegistry!=='function' || !isBuiltinProtocol('openai-responses')) throw Error('actual_sdk_identity_lost');
 export default{manifest:{id:'actual',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Actual SDK',{complete(){throw new ModelProtocolError('http',{status:503})}})}}`
	if err = os.WriteFile(plugin, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enable(context.Background(), "actual", plugin, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	_, err = callProtocolFixture(host, context.Background(), protocol.ID, protocol.Generation, "complete", []any{map[string]any{}})
	failure, ok := ClassifyProtocolFailure(err)
	if !ok || !failure.Retryable || failure.Status == nil || *failure.Status != 503 {
		t.Fatal("actual SDK import identity lost", failure, err)
	}
}

func TestV1SDKMappingUsesRealBarePackageResolution(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "node_modules", "legacy-sdk")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"package.json": `{"name":"legacy-sdk","type":"module","exports":"./index.mjs"}`, "index.mjs": `export const marker='actual-package'`} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// This exact path is an allowed module alias, but the bare package name must
	// still resolve to node_modules rather than a guessed importer-relative URL.
	alias := (&url.URL{Scheme: "file", Path: filepath.Join(root, "legacy-sdk")}).String()
	host, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{ModelProtocolModuleURLs: []string{alias}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	path := filepath.Join(root, "plugin.mjs")
	source := `import {marker} from 'legacy-sdk';if(marker!=='actual-package')throw Error('guessed_sdk_path');export default{manifest:{id:'package',configSchema:{type:'object'},capabilities:[]},setup(){}}`
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enable(context.Background(), "package", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
