package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type remainingWriteOracle struct {
	Rows []struct {
		Label, Raw, Payload             string
		Before, After, Repeated, Forced Head
	}
	Rejected []struct{ Label, Raw, Error string }
	Special  []struct {
		Kind, Name, Error string
		Path              []string
	}
}

func readRemainingWriteOracle(t *testing.T) remainingWriteOracle {
	t.Helper()
	b, err := os.ReadFile("testdata/remaining-write-events-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var f remainingWriteOracle
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) < 200 || len(f.Rejected) < 1000 || len(f.Special) != 21 {
		t.Fatal("oracle inventory shrank", len(f.Rows), len(f.Rejected), len(f.Special))
	}
	return f
}

// Test-only decoding constructs sealed typed intents from the oracle corpus.
// Production has no generic map/JSON-to-Revocation escape hatch. Unlike
// encoding/json this decoder preserves WTF-8, refuses unknown/missing fields,
// and distinguishes optional omission from illegal explicit null.
func remainingOracleIntent(raw string) (Revocation, error) {
	value, err := jsjson.Parse(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(*jsjson.Object)
	if !ok {
		return nil, fmt.Errorf("object required")
	}
	kind, _ := object.Get("kind")
	var target Revocation
	switch kind {
	case "external_cost_receipt":
		target = &ExternalCostReceipt{}
	case "experience_trial":
		target = &ExperienceTrial{}
	case "semantic_mutation":
		target = &SemanticMutation{}
	case "identity_mutation":
		target = &IdentityMutation{}
	case "working_set_pin":
		target = &WorkingSetPin{}
	case "commitment_head":
		target = &CommitmentHead{}
	case "commitment_delete":
		target = &CommitmentDelete{}
	default:
		return nil, fmt.Errorf("kind not supported")
	}
	fields := new(jsjson.Object)
	for _, m := range object.Members() {
		if m.Key == "kind" {
			continue
		}
		if m.Key == "schemaVersion" && kind != "commitment_head" {
			if m.Value != float64(1) {
				return nil, fmt.Errorf("fixed schema")
			}
			continue
		}
		_ = fields.Set(m.Key, m.Value)
	}
	if kind != "external_cost_receipt" && kind != "commitment_head" {
		if _, has := object.Get("schemaVersion"); !has {
			return nil, fmt.Errorf("missing schema")
		}
	}
	if err = decodeRemainingOracle(reflect.ValueOf(target).Elem(), fields); err != nil {
		return nil, err
	}
	return target, nil
}
func decodeRemainingOracle(target reflect.Value, value any) error {
	if target.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		target.Set(reflect.New(target.Type().Elem()))
		return decodeRemainingOracle(target.Elem(), value)
	}
	switch target.Kind() {
	case reflect.Struct:
		o, ok := value.(*jsjson.Object)
		if !ok {
			return fmt.Errorf("object required")
		}
		used := 0
		for i := 0; i < target.NumField(); i++ {
			field := target.Type().Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			v, has := o.Get(name)
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if !has {
				if optional {
					continue
				}
				return fmt.Errorf("missing %s", name)
			}
			if optional && v == nil {
				return fmt.Errorf("null optional %s", name)
			}
			if err := decodeRemainingOracle(target.Field(i), v); err != nil {
				return err
			}
			used++
		}
		if used != o.Len() {
			return fmt.Errorf("unknown property")
		}
	case reflect.Slice:
		a, ok := value.([]any)
		if !ok {
			return fmt.Errorf("array required")
		}
		target.Set(reflect.MakeSlice(target.Type(), len(a), len(a)))
		for i, v := range a {
			if err := decodeRemainingOracle(target.Index(i), v); err != nil {
				return err
			}
		}
	case reflect.String:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("string required")
		}
		target.SetString(s)
	case reflect.Bool:
		b, ok := value.(bool)
		if !ok {
			return fmt.Errorf("bool required")
		}
		target.SetBool(b)
	case reflect.Float64:
		n, ok := value.(float64)
		if !ok {
			return fmt.Errorf("number required")
		}
		target.SetFloat(n)
	case reflect.Int:
		n, ok := value.(float64)
		if !ok || n != math.Trunc(n) || n < -10000 || n > 10000 {
			return fmt.Errorf("integer schema required")
		}
		target.SetInt(int64(n))
	default:
		return fmt.Errorf("unsupported type %v", target.Kind())
	}
	return nil
}

func TestRemainingTypedWritesMatchActualNode(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	f := newWriterFixture(t)
	ctx := context.Background()
	head := f.gate.Head
	kinds := map[string]bool{}
	for _, row := range oracle.Rows {
		t.Run(row.Label, func(t *testing.T) {
			intent, err := remainingOracleIntent(row.Raw)
			if err != nil {
				t.Fatal(err)
			}
			kind, _ := intent.recoveryRecord()
			kinds[kind] = true
			if head != row.Before {
				t.Fatalf("before %+v want %+v", head, row.Before)
			}
			head, err = f.authority.Append(ctx, intent, head, false)
			if err != nil || head != row.After {
				t.Fatalf("append %+v want %+v: %v", head, row.After, err)
			}
			var payload string
			if err = f.external.QueryRow("SELECT payload FROM revocations WHERE payload=?", row.Payload).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if payload != row.Payload {
				t.Fatalf("payload\n%s\nwant\n%s", payload, row.Payload)
			}
			repeated, err := f.authority.Append(ctx, intent, head, false)
			if err != nil || repeated != row.Repeated || repeated != head {
				t.Fatal("dedup", repeated, head, err)
			}
			head, err = f.authority.Append(ctx, intent, repeated, true)
			if err != nil || head != row.Forced {
				t.Fatal("force", head, row.Forced, err)
			}
		})
	}
	if len(kinds) != 7 {
		t.Fatal("missing kinds", kinds)
	}
	if f.authority.Scans() != 1 {
		t.Fatal("own writes rescanned history")
	}
}

func TestRemainingTypedPrepareWriteMatchesActualNode(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	// Exercise each kind/version (including a nested privacy marker) through the
	// real reserved main transaction, not only the standalone append method.
	seen := map[string]bool{}
	for _, row := range oracle.Rows {
		intent, err := remainingOracleIntent(row.Raw)
		if err != nil {
			t.Fatal(err)
		}
		kind, _ := intent.recoveryRecord()
		key := kind
		if e, ok := intent.(*CommitmentHead); ok {
			key = fmt.Sprintf("%s:%d:%t", kind, e.SchemaVersion, e.PrivacyMarker != nil)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		t.Run(key, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx := context.Background()
			calls := 0
			prepare := func(*MainRead) (WritePlan, error) {
				return WritePlan{Intents: []Revocation{intent}, MinimumReader: ReaderCapability, Apply: func(*MainMutation) error { calls++; return nil }}, nil
			}
			head, err := f.authority.PrepareWrite(ctx, f.main, prepare)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := f.authority.PrepareWrite(ctx, f.main, prepare)
			if err != nil || head != repeated || calls != 2 {
				t.Fatal("prepare dedup", head, repeated, calls, err)
			}
			var payload string
			if err = f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=1").Scan(&payload); err != nil || payload != row.Payload {
				t.Fatal("prepare payload", payload, err)
			}
			gate, err := ReadGate(ctx, f.main)
			if err != nil || gate.Head != head {
				t.Fatal("prepare ACK", gate, head, err)
			}
		})
	}
	if len(seen) < 9 {
		t.Fatal("version/marker coverage", seen)
	}
}

func TestRemainingTypedBadInputsFailBeforeAuthorityOrMain(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	f := newWriterFixture(t)
	ctx := context.Background()
	count := 0
	kinds := map[string]bool{}
	for _, row := range oracle.Rejected {
		intent, err := remainingOracleIntent(row.Raw)
		if err != nil {
			continue
		} // Invalid shapes cannot be represented by sealed types.
		count++
		kind, _ := intent.recoveryRecord()
		kinds[kind] = true
		if _, err = f.authority.Append(ctx, intent, f.gate.Head, false); err == nil {
			t.Fatalf("bad typed input accepted: %s %s", row.Label, row.Raw)
		}
		called := false
		_, err = f.authority.PrepareWrite(ctx, f.main, func(*MainRead) (WritePlan, error) {
			return WritePlan{Intents: []Revocation{intent}, MinimumReader: ReaderCapability, Apply: func(*MainMutation) error { called = true; return nil }}, nil
		})
		if err == nil || called {
			t.Fatalf("bad preparation mutated main: %s %v", row.Label, err)
		}
	}
	if count < 200 || len(kinds) != 7 {
		t.Fatal("invalid value coverage", count, kinds)
	}
	head, err := f.authority.Head(ctx)
	if err != nil || head != f.gate.Head {
		t.Fatal("bad inputs changed head", head, err)
	}
}

func TestRemainingTypedNonfiniteAndNegativeZero(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	bases := map[string]string{}
	for _, row := range oracle.Rows {
		if strings.HasSuffix(row.Label, ":base") {
			intent, _ := remainingOracleIntent(row.Raw)
			kind, _ := intent.recoveryRecord()
			bases[kind] = row.Raw
		}
	}
	f := newWriterFixture(t)
	ctx := context.Background()
	for _, entry := range oracle.Special {
		t.Run(entry.Kind+entry.Name, func(t *testing.T) {
			intent, err := remainingOracleIntent(bases[entry.Kind])
			if err != nil {
				t.Fatal(err)
			}
			field := reflect.ValueOf(intent).Elem()
			for _, name := range entry.Path {
				if field.Kind() == reflect.Pointer {
					field = field.Elem()
				}
				typ := field.Type()
				found := false
				for i := 0; i < field.NumField(); i++ {
					if strings.Split(typ.Field(i).Tag.Get("json"), ",")[0] == name {
						field = field.Field(i)
						found = true
						break
					}
				}
				if !found {
					t.Fatal("missing path", name)
				}
			}
			n := math.NaN()
			if entry.Name == "Infinity" {
				n = math.Inf(1)
			}
			if entry.Name == "-Infinity" {
				n = math.Inf(-1)
			}
			if field.Kind() == reflect.Pointer {
				field.Set(reflect.New(field.Type().Elem()))
				field = field.Elem()
			}
			field.SetFloat(n)
			if _, err = f.authority.Append(ctx, intent, f.gate.Head, false); err == nil {
				t.Fatal("nonfinite accepted")
			}
			called := false
			_, err = f.authority.PrepareWrite(ctx, f.main, func(*MainRead) (WritePlan, error) {
				return WritePlan{[]Revocation{intent}, ReaderCapability, func(*MainMutation) error { called = true; return nil }}, nil
			})
			if err == nil || called {
				t.Fatal("nonfinite preparation accepted", err)
			}
		})
	}
	// Explicit negative zero remains accepted for a nonnegative source number,
	// then serializes to source JSON.stringify's ordinary zero before hashing.
	zero := math.Copysign(0, -1)
	intent := CommitmentDelete{SceneID: "s", ID: "c", DeletionEpoch: zero}
	a, err := f.authority.Append(ctx, intent, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	intent.DeletionEpoch = 0
	b, err := f.authority.Append(ctx, intent, a, false)
	if err != nil || a != b {
		t.Fatal("negative zero did not deduplicate", err)
	}
}

func TestRemainingTypedWritesNeverNormalizeHistoricalPayloads(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	seen := map[string]bool{}
	for _, row := range oracle.Rows {
		intent, err := remainingOracleIntent(row.Raw)
		if err != nil {
			t.Fatal(err)
		}
		kind, _ := intent.recoveryRecord()
		if seen[kind] {
			continue
		}
		seen[kind] = true
		t.Run(kind, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx := context.Background()
			historical := " " + row.Raw + "\n"
			previous := f.gate.Head
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s|1|%s|%s", previous.InstanceID, previous.Hash, historical)))
			before := Head{previous.InstanceID, 1, fmt.Sprintf("%x", sum)}
			if _, err = f.external.Exec("INSERT INTO revocations VALUES(?,?,?,?)", 1, previous.Hash, historical, before.Hash); err != nil {
				t.Fatal(err)
			}
			after, err := f.authority.Append(ctx, intent, before, false)
			if err != nil || after.Sequence != 2 {
				t.Fatal("historical append", after, err)
			}
			var raw, digest string
			if err = f.external.QueryRow("SELECT payload,hash FROM revocations WHERE sequence=1").Scan(&raw, &digest); err != nil || raw != historical || digest != before.Hash {
				t.Fatal("history changed", raw, digest, err)
			}
			if err = f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=2").Scan(&raw); err != nil || raw != row.Payload {
				t.Fatal("canonical append", raw, err)
			}
		})
	}
}
func TestExternalCostIntentConstructorRejectsUnknownAndInvalidData(t *testing.T) {
	oracle := readRemainingWriteOracle(t)
	checked := 0
	for _, row := range oracle.Rejected {
		value, err := jsjson.Parse(row.Raw)
		if err != nil {
			continue
		}
		event, ok := value.(*jsjson.Object)
		if !ok || event.Len() != 2 {
			continue
		}
		kind, _ := event.Get("kind")
		if kind != "external_cost_receipt" {
			continue
		}
		receipt, has := event.Get("receipt")
		if !has {
			continue
		}
		raw, err := jsjson.Stringify(receipt)
		if err != nil {
			continue
		}
		checked++
		if _, err = ParseExternalCostReceiptIntent(raw); err == nil {
			t.Fatal("invalid constructor input accepted", row.Label, raw)
		}
	}
	if checked < 200 {
		t.Fatal("constructor invalid coverage", checked)
	}
	for _, row := range oracle.Rows {
		intent, err := remainingOracleIntent(row.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := intent.(*ExternalCostReceipt); !ok {
			continue
		}
		value, _ := jsjson.Parse(row.Raw)
		receipt, _ := value.(*jsjson.Object).Get("receipt")
		raw, _ := jsjson.Stringify(receipt)
		parsed, err := ParseExternalCostReceiptIntent(raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encodeRevocation(parsed)
		if err != nil || encoded.payload != row.Payload {
			t.Fatal("constructor bytes", row.Label, encoded.payload, "want", row.Payload, err)
		}
	}
}
