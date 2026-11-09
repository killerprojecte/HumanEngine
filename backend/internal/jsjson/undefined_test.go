package jsjson

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestNativeUndefinedActualSourceOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/native-undefined.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Serialization struct {
			Object                  string
			Keys                    []string
			Array                   string
			RootKind                string
			NullValue               string
			EmptyObject             string
			ParsedNullIsUndefined   bool
			ParsedStringIsUndefined bool
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	nested, _ := NewObject(Member{"missing", Undefined}, Member{"kept", true})
	own, _ := NewObject(Member{"before", Undefined}, Member{"kept", nil}, Member{"after", Undefined}, Member{"nested", nested})
	got, err := Stringify(own)
	if err != nil || got != fixture.Serialization.Object {
		t.Fatalf("object: %s %v", got, err)
	}
	got, err = Stringify([]any{Undefined, nil, own})
	if err != nil || got != fixture.Serialization.Array {
		t.Fatalf("array: %s %v", got, err)
	}
	if !reflect.DeepEqual(own.Keys(), fixture.Serialization.Keys) {
		t.Fatal("own keys lost", own.Keys())
	}
	for _, o := range []*Object{own, own.Clone()} {
		v, ok := o.Get("before")
		if !ok || !IsUndefined(v) {
			t.Fatal("clone lost own undefined")
		}
	}
	if fixture.Serialization.RootKind != "undefined" {
		t.Fatal("unexpected source root")
	}
	if got, err := Stringify(Undefined); got != "" || !errors.Is(err, ErrUndefinedRoot) {
		t.Fatal(got, err)
	}
	if got, err := Marshal(Undefined); got != nil || !errors.Is(err, ErrUndefinedRoot) {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"null", `"undefined"`, "{}", "[null]"} {
		v, err := Parse(raw)
		if err != nil || IsUndefined(v) {
			t.Fatalf("parse %s synthesized undefined: %v", raw, err)
		}
	}
	if fixture.Serialization.ParsedNullIsUndefined || fixture.Serialization.ParsedStringIsUndefined {
		t.Fatal("source parser mismatch")
	}
	if got, _ := Stringify(nil); got != fixture.Serialization.NullValue {
		t.Fatal(got)
	}
	if got, _ := Stringify(new(Object)); got != fixture.Serialization.EmptyObject {
		t.Fatal(got)
	}
	deleted := own.Clone()
	deleted.Delete("before")
	if reflect.DeepEqual(own, deleted) || reflect.DeepEqual(Undefined, nil) {
		t.Fatal("missing/null conflated")
	}
	// Native undefined is the only new value kind; general structs/maps remain rejected.
	for _, v := range []any{struct{}{}, map[string]any{}, int(1)} {
		if _, err := Stringify(v); err == nil {
			t.Fatalf("unsupported accepted: %T", v)
		}
	}
	cycle := new(Object)
	_ = cycle.Set("undefined", Undefined)
	_ = cycle.Set("self", cycle)
	if _, err := Stringify(cycle); err == nil {
		t.Fatal("cycle accepted")
	}
}
