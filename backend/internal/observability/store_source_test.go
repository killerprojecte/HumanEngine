package observability

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type storeOracleDigest struct {
	Bytes  int     `json:"bytes"`
	SHA256 string  `json:"sha256"`
	Text   *string `json:"text,omitempty"`
}

func digestStoreText(text string) storeOracleDigest {
	d := storeOracleDigest{Bytes: len(text), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))}
	if len(text) < 16384 {
		d.Text = &text
	}
	return d
}

type storeOracleCapture struct {
	Sink     string `json:"sink"`
	Priority bool   `json:"priority"`
	storeOracleDigest
}
type storeOracleCheckpoint struct {
	Name     string             `json:"name"`
	Status   StoreLoggingStatus `json:"status"`
	Retained storeOracleDigest  `json:"retained"`
	RowBytes []int              `json:"rowBytes"`
}
type storeOracleBehavior struct {
	AcquireThrow, EnqueueThrow, Reject, SyncThrow, Incomplete bool
	SyncAdvance                                               int64
	Degraded                                                  []int64
}
type storeOracleStep struct {
	Kind, Name, Level string
	Fields            json.RawMessage
	Millis            int64
}
type storeOracleSpec struct {
	Name, Path, Special string
	Env, Flow           map[string]string
	Behavior            map[string]storeOracleBehavior
	Steps               []storeOracleStep
}
type storeOracleCase struct {
	Name               string
	Spec               *storeOracleSpec
	Directory          *string
	Operations         []string
	Captures           []storeOracleCapture
	Checkpoints        []storeOracleCheckpoint
	Results            [][]logdelivery.FlushResult
	SerializationCalls []int
	ShutdownComplete   *bool
}
type storeOracleOutput struct {
	Directory          *string
	Operations         []string
	Captures           []storeOracleCapture
	Checkpoints        []storeOracleCheckpoint
	Results            [][]logdelivery.FlushResult
	SerializationCalls []int
}
type storeOracleLease struct {
	kind       string
	behavior   storeOracleBehavior
	status     logdelivery.Status
	result     logdelivery.FlushResult
	released   bool
	operations *[]string
	captures   *[]storeOracleCapture
	now        *time.Time
}

func (s *storeOracleLease) Status() logdelivery.Status { return s.status }
func (s *storeOracleLease) Enqueue(record string, priority bool) bool {
	*s.operations = append(*s.operations, "enqueue-"+s.kind)
	*s.captures = append(*s.captures, storeOracleCapture{Sink: s.kind, Priority: priority, storeOracleDigest: digestStoreText(record)})
	if s.behavior.EnqueueThrow {
		panic("private enqueue failure")
	}
	return !s.behavior.Reject
}
func (s *storeOracleLease) beginClose() logClose {
	*s.operations = append(*s.operations, "close-"+s.kind)
	s.released = true
	return settledLogClose{result: &s.result}
}
func (s *storeOracleLease) CloseSync(timeout time.Duration) *logdelivery.FlushResult {
	*s.operations = append(*s.operations, fmt.Sprintf("sync-%s:%d", s.kind, timeout.Milliseconds()))
	if s.released {
		return nil
	}
	s.released = true
	*s.now = s.now.Add(time.Duration(s.behavior.SyncAdvance) * time.Millisecond)
	if s.behavior.SyncThrow {
		panic("private sync failure")
	}
	return &s.result
}
func (s *storeOracleLease) WaitPhysicalRelease(context.Context) error { return nil }
func storeOracleFields(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range value.(*jsjson.Object).Members() {
		out[member.Key] = member.Value
	}
	return out
}
func equalStoreJSON(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		t.Fatal("bad test JSON")
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("actual:\n%s\nsource:\n%s", a, b)
	}
}

// Store orchestration is replayed at the same explicit lease boundary as the
// actual-source fixture. Status/result values are fixture INPUTS here, not
// invented production counters. Existing channel25 tests and physical native
// tests prove those independent lower-layer contracts. No DB cleanup claim is
// made by this logger-only replay: its three source callbacks are omitted below
// and belong to the later owning-application integration test.
func TestSourceStoreBoundaryOracle(t *testing.T) {
	bytes, err := os.ReadFile("log-store-memory-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases               []storeOracleCase
		BoundaryInputSHA256 string
		FixedAt, FixedID    string
	}
	if err = json.Unmarshal(bytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 26 {
		t.Fatal(len(fixture.Cases))
	}
	channelBytes, err := os.ReadFile("log-channel-memory-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(channelBytes)) != fixture.BoundaryInputSHA256 {
		t.Fatal("boundary source input changed")
	}
	var channels struct{ Cases []json.RawMessage }
	if err = json.Unmarshal(channelBytes, &channels); err != nil {
		t.Fatal(err)
	}
	if len(channels.Cases) != 25 {
		t.Fatal("source channel case count", len(channels.Cases))
	}
	// Only case0 supplies the two explicit boundary inputs. Other source cases
	// intentionally use different shapes for their own independent observations.
	var defaults struct {
		Initial []logdelivery.Status
		Closed  []logdelivery.FlushResult
	}
	if err = json.Unmarshal(channels.Cases[0], &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.Initial) != 2 || len(defaults.Closed) != 2 {
		t.Fatal("source default inputs changed")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Spec == nil {
				replayStoreAllSettled(t, c)
				return
			}
			spec := c.Spec
			now, err := time.Parse(time.RFC3339Nano, fixture.FixedAt)
			if err != nil {
				t.Fatal(err)
			}
			l := NewStoreLogger(StoreLoggerOptions{Database: spec.Path, LookupEnv: func(key string) (string, bool) { value, ok := spec.Env[key]; return value, ok }})
			l.now = func() time.Time { return now }
			l.newID = func() (string, error) { return fixture.FixedID, nil }
			got := storeOracleOutput{Operations: []string{}, Captures: []storeOracleCapture{}, Checkpoints: []storeOracleCheckpoint{}, Results: [][]logdelivery.FlushResult{}, SerializationCalls: []int{}}
			if l.directory != "" {
				value := l.directory
				got.Directory = &value
			}
			l.acquire = func(directory string) (logLease, error) {
				kind, index := "stdout", 0
				if directory != "" {
					kind, index = "file", 1
				}
				behavior := spec.Behavior[kind]
				got.Operations = append(got.Operations, "acquire-"+kind)
				if behavior.AcquireThrow {
					return nil, fmt.Errorf("private acquire failure")
				}
				status := defaults.Initial[index]
				if behavior.Degraded != nil {
					status.State = "degraded"
					status.RejectedRecords, status.FailedRecords = behavior.Degraded[0], behavior.Degraded[1]
					failure := "sink_failed"
					status.LastFailure = &failure
				}
				result := defaults.Closed[index]
				result.Complete = !behavior.Incomplete
				result.Status = status
				return &storeOracleLease{kind: kind, behavior: behavior, status: status, result: result, operations: &got.Operations, captures: &got.Captures, now: &now}, nil
			}
			counted := new(countedLogJSON)
			for _, step := range spec.Steps {
				switch step.Kind {
				case "emit":
					fields := storeOracleFields(t, step.Fields)
					switch spec.Special {
					case "numbers":
						fields = map[string]any{"nan": math.NaN(), "negativeInfinity": math.Inf(-1), "positiveInfinity": math.Inf(1), "surrogate": jsjson.FromUTF16([]uint16{0xd800})}
					case "toJSON":
						fields = map[string]any{"custom": counted}
					case "bigint":
						fields = map[string]any{"bad": big.NewInt(1)}
					case "cycle":
						cycle := map[string]any{}
						cycle["self"] = cycle
						fields = map[string]any{"bad": cycle}
					case "oversized":
						fields = map[string]any{"extra": strings.Repeat("x", 4*1024*1024)}
					}
					event := Event{Level: step.Level, Module: "synthetic", Code: "ordinary", RequestID: spec.Flow["requestId"], TraceID: spec.Flow["traceId"], SceneID: spec.Flow["sceneId"], TurnID: spec.Flow["turnId"], Stage: spec.Flow["stage"], Fields: fields}
					_ = l.Emit(event)
					got.SerializationCalls = append(got.SerializationCalls, counted.calls)
				case "advance":
					now = now.Add(time.Duration(step.Millis) * time.Millisecond)
				case "status":
					checkpoint := storeOracleCheckpoint{Name: step.Name, Status: l.LoggingStatus(), Retained: digestStoreText(string(l.RetainedJSON())), RowBytes: []int{}}
					l.mu.Lock()
					for _, row := range l.retained.rows {
						checkpoint.RowBytes = append(checkpoint.RowBytes, len(row.json))
					}
					l.mu.Unlock()
					got.Checkpoints = append(got.Checkpoints, checkpoint)
				case "close":
					results, err := l.CloseLogs(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					got.Results = append(got.Results, results)
				case "syncClose":
					l.CloseLogsSync(logdelivery.DefaultTimeout)
				default:
					t.Fatal(step.Kind)
				}
			}
			// Compare every log orchestration operation, each exact sink/retained byte
			// sequence/digest/length, every status field and every close result field.
			operations := []string{}
			for _, op := range c.Operations {
				if op != "clear-diagnostics" && op != "close-authority" && op != "close-db" {
					operations = append(operations, op)
				}
			}
			want := storeOracleOutput{Directory: c.Directory, Operations: operations, Captures: c.Captures, Checkpoints: c.Checkpoints, Results: c.Results, SerializationCalls: c.SerializationCalls}
			equalStoreJSON(t, got, want)
		})
	}
}
func replayStoreAllSettled(t *testing.T, c storeOracleCase) {
	l := testStore(t)
	trace := new(storeTrace)
	gate := make(chan struct{})
	begun := make(chan struct{})
	done := make(chan error, 1)
	failing := "stdout"
	if strings.Contains(c.Name, "-file-") {
		failing = "file"
	}
	makeLease := func(name string) *controlledLogLease {
		return &controlledLogLease{name: name, trace: trace, begin: func() logClose {
			if name == "stdout" {
				close(begun)
			}
			if name == failing {
				return settledLogClose{err: fmt.Errorf("private close error")}
			}
			return heldLogClose{name: name, trace: trace, gate: gate}
		}}
	}
	l.file, l.stdout = makeLease("file"), makeLease("stdout")
	go func() {
		_, err := l.CloseLogs(context.Background())
		if err != nil {
			trace.add("store-reject")
		}
		done <- err
	}()
	storeSignal(t, begun)
	select {
	case <-done:
		t.Fatal("allSettled returned before other sink")
	default:
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("lost source rejection")
	}
	equalStoreJSON(t, trace.snapshot(), c.Operations)
	equalStoreJSON(t, l.LoggingStatus().ShutdownComplete, c.ShutdownComplete)
}
