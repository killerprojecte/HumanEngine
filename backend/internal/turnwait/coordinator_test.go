package turnwait

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
)

// fixtureNumber transports JS nonfinite numbers without changing their meaning
// to JSON null. These values exercise configuration and WAIT-result rejection.
type fixtureNumber float64

func (n *fixtureNumber) UnmarshalJSON(raw []byte) error {
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		*n = fixtureNumber(value)
		return nil
	}
	var tag struct {
		Number string `json:"number"`
	}
	if err := json.Unmarshal(raw, &tag); err != nil {
		return err
	}
	switch tag.Number {
	case "NaN":
		*n = fixtureNumber(math.NaN())
	case "Infinity":
		*n = fixtureNumber(math.Inf(1))
	case "-Infinity":
		*n = fixtureNumber(math.Inf(-1))
	default:
		return fmt.Errorf("unknown fixture number %q", tag.Number)
	}
	return nil
}

type fixtureConfig struct {
	QuietMS          fixtureNumber `json:"quietMs"`
	MaxWaitMS        fixtureNumber `json:"maxWaitMs"`
	AddressedQuietMS fixtureNumber `json:"addressedQuietMs"`
	MaxModelWaitMS   fixtureNumber `json:"maxModelWaitMs"`
	MaxPending       fixtureNumber `json:"maxPending"`
}

func (c fixtureConfig) config() Config {
	return Config{QuietMS: float64(c.QuietMS), MaxWaitMS: float64(c.MaxWaitMS),
		AddressedQuietMS: float64(c.AddressedQuietMS), MaxModelWaitMS: float64(c.MaxModelWaitMS),
		MaxPending: float64(c.MaxPending)}
}

type fixtureStep struct {
	At        float64        `json:"at"`
	Op        string         `json:"op"`
	ID        []uint16       `json:"idUTF16"`
	IDs       [][]uint16     `json:"idsUTF16"`
	Save      string         `json:"save"`
	Handle    string         `json:"handle"`
	WaitUntil *fixtureNumber `json:"waitUntil"`
	Config    fixtureConfig  `json:"config"`
	Field     string         `json:"field"`
	Value     fixtureNumber  `json:"value"`
	Event     struct {
		ID         []uint16 `json:"idUTF16"`
		Meaningful bool     `json:"meaningful"`
		Addressed  bool     `json:"addressed"`
	} `json:"event"`
}

type traceFixture struct {
	Version      int    `json:"version"`
	Baseline     string `json:"baseline"`
	Source       string `json:"source"`
	SourceSHA256 string `json:"sourceSHA256"`
	Node         string `json:"node"`
	Invalid      []struct {
		Config fixtureConfig `json:"config"`
		Error  string        `json:"error"`
	} `json:"invalidConfigurations"`
	Traces []struct {
		Name   string        `json:"name"`
		Config fixtureConfig `json:"config"`
		Steps  []struct {
			Input    fixtureStep     `json:"input"`
			Expected json.RawMessage `json:"expected"`
		} `json:"steps"`
	} `json:"traces"`
}

// IDs are opaque. A lossless test-only UTF-16 wire representation avoids the
// standard JSON decoder merging different lone-surrogate IDs into U+FFFD.
// This does not implement or claim production JSON-string migration parity.
func fixtureID(units []uint16) string {
	bytes := make([]byte, len(units)*2)
	for i, value := range units {
		binary.LittleEndian.PutUint16(bytes[i*2:], value)
	}
	return string(bytes)
}

func fixtureIDs(values [][]uint16) []string {
	ids := make([]string, len(values))
	for i, value := range values {
		ids[i] = fixtureID(value)
	}
	return ids
}

func wireIDs(ids []string) [][]uint16 {
	values := make([][]uint16, len(ids))
	for i, id := range ids {
		values[i] = make([]uint16, len(id)/2)
		for j := range values[i] {
			values[i][j] = binary.LittleEndian.Uint16([]byte(id[j*2:]))
		}
	}
	return values
}

func wireWake(wake *Wake) any {
	return map[string]any{"kind": wake.Kind, "generation": wake.Generation,
		"eventIdsUTF16": wireIDs(wake.EventIDs), "reason": wake.Reason}
}

func optional(value any, present bool) map[string]any {
	if !present {
		return map[string]any{"present": false}
	}
	return map[string]any{"present": true, "value": value}
}

func TestNodeDifferentialTraces(t *testing.T) {
	data, err := os.ReadFile("testdata/traces-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture traceFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Baseline != "24fe8a773d87f2f09996e259fca46524c2c02928" || fixture.Node != "v24.22.1" || fixture.Source != "src/runtime/turn-wait.ts" {
		t.Fatal("unexpected oracle identity")
	}
	source, err := os.ReadFile("../../../" + fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(source)
	if hex.EncodeToString(hash[:]) != fixture.SourceSHA256 || fixture.SourceSHA256 != "bf01e8b12103b76a0954546f7882137a531d9efff4961bf38b0cb627df3e1217" {
		t.Fatal("TypeScript oracle changed without baseline review")
	}
	if len(fixture.Traces) < 40 || len(fixture.Invalid) != 32 {
		t.Fatal("fixture coverage unexpectedly reduced")
	}
	for i, invalid := range fixture.Invalid {
		t.Run(fmt.Sprintf("invalid_configuration_%d", i), func(t *testing.T) {
			_, err := New(invalid.Config.config(), func() float64 { return 0 })
			if err == nil || err.Error() != invalid.Error {
				t.Fatalf("got %v, want %s", err, invalid.Error)
			}
		})
	}
	for _, trace := range fixture.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			now := 0.0
			supplied := trace.Config.config()
			queue, err := New(supplied, func() float64 { return now })
			if err != nil {
				t.Fatal(err)
			}
			handles := make(map[string]*Wake)
			for index, step := range trace.Steps {
				input := step.Input
				now = input.At
				var result any
				var operationError error
				switch input.Op {
				case "observe":
					var outcome string
					outcome, operationError = queue.Observe(Event{ID: fixtureID(input.Event.ID), Meaningful: input.Event.Meaningful, Addressed: input.Event.Addressed})
					if operationError == nil {
						result = outcome
					}
				case "recall":
					queue.Recall(fixtureID(input.ID))
				case "replace":
					operationError = queue.Replace(fixtureID(input.ID))
				case "takeReady":
					if wake := queue.TakeReady(); wake != nil {
						result = wireWake(wake)
						if input.Save != "" {
							handles[input.Save] = wake
						}
					}
				case "finish":
					wake, exists := handles[input.Handle]
					if !exists {
						t.Fatalf("step %d: missing wake %q", index, input.Handle)
					}
					finishResult := Result{}
					if input.WaitUntil != nil {
						deadline := float64(*input.WaitUntil)
						finishResult.WaitUntil = &deadline
					}
					result = queue.Finish(wake, finishResult)
				case "cancel":
					queue.Cancel()
				case "configure":
					supplied = input.Config.config()
					operationError = queue.Configure(supplied)
				case "mutateConfig":
					if input.Field != "quietMs" {
						t.Fatalf("step %d: unknown config mutation %q", index, input.Field)
					}
					supplied.QuietMS = float64(input.Value)
				case "restart":
					supplied = trace.Config.config()
					queue, operationError = New(supplied, func() float64 { return now })
					if operationError != nil {
						t.Fatal(operationError)
					}
				case "cloneWake":
					clone := *handles[input.Handle]
					clone.EventIDs = append([]string{}, clone.EventIDs...)
					handles[input.Save] = &clone
				case "mutateWakeIds":
					handles[input.Handle].EventIDs = fixtureIDs(input.IDs)
				case "mutateWakeGeneration":
					handles[input.Handle].Generation = float64(input.Value)
				case "mutateRetained":
					retained := queue.RetainedEventIDs()
					_ = append(retained, "outside-mutation")
				case "snapshot":
				default:
					t.Fatalf("unknown operation %q", input.Op)
				}
				current := make(map[string]bool)
				for name, wake := range handles {
					current[name] = queue.IsCurrent(wake)
				}
				next, nextPresent := queue.NextWakeAt()
				addressed, addressedPresent := queue.PendingAddressed()
				observed := map[string]any{
					"result": optional(result, result != nil), "nextWakeAt": optional(next, nextPresent),
					"pendingAddressed": optional(addressed, addressedPresent), "retention": queue.Retention(),
					"retainedEventIdsUTF16": wireIDs(queue.RetainedEventIDs()), "current": current,
				}
				if operationError != nil {
					observed["error"] = operationError.Error()
				}
				actualJSON, err := json.Marshal(observed)
				if err != nil {
					t.Fatal(err)
				}
				var actual, expected any
				if err := json.Unmarshal(actualJSON, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(step.Expected, &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("step %d (%s at %v)\ngot  %s\nwant %s", index, input.Op, input.At, actualJSON, step.Expected)
				}
			}
		})
	}
}

func TestCoordinatorIsolationAndWakeOwnership(t *testing.T) {
	config := Config{QuietMS: 0, MaxWaitMS: 0, MaxPending: 8}
	one, err := New(config, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	two, err := New(config, func() float64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	for _, coordinator := range []*Coordinator{one, two} {
		if outcome, err := coordinator.Observe(Event{ID: "same-scene-local-id", Meaningful: true}); outcome != "accepted" || err != nil {
			t.Fatal(outcome, err)
		}
	}
	first, second := one.TakeReady(), two.TakeReady()
	if one.IsCurrent(second) || two.IsCurrent(first) || one.Finish(second, Result{}) {
		t.Fatal("a foreign coordinator's wake acquired ownership")
	}
	one.Recall("same-scene-local-id")
	if one.IsCurrent(first) || !two.IsCurrent(second) {
		t.Fatal("recall crossed coordinator boundaries")
	}
	if !one.Finish(first, Result{}) || !two.Finish(second, Result{}) {
		t.Fatal("owner could not release active wake")
	}
	if one.IsCurrent(nil) || one.Finish(nil, Result{}) {
		t.Fatal("absent wake acquired ownership")
	}
}

func TestClockReadsOnlyAtLegacyTransitions(t *testing.T) {
	reads := 0
	queue, err := New(Config{MaxPending: 1}, func() float64 { reads++; return 0 })
	if err != nil {
		t.Fatal(err)
	}
	queue.Observe(Event{ID: "source", Meaningful: true})
	queue.Observe(Event{ID: "source", Meaningful: true})
	queue.Observe(Event{ID: "full", Meaningful: true})
	queue.NextWakeAt()
	queue.PendingAddressed()
	queue.Retention()
	queue.RetainedEventIDs()
	if reads != 1 {
		t.Fatalf("clock read outside accepted ingress: %d", reads)
	}
	wake := queue.TakeReady()
	queue.TakeReady()
	queue.IsCurrent(wake)
	if reads != 2 {
		t.Fatalf("clock read while active: %d", reads)
	}
	queue.Finish(wake, Result{})
	if reads != 2 {
		t.Fatalf("terminal finish read clock: %d", reads)
	}
}
