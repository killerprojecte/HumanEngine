package jsjson

import (
	"errors"
	"testing"
)

func TestMarshalReturnsIndependentCanonicalBytes(t *testing.T) {
	const raw = `{"s":"\ud800\u0000é","m":{"10":"ten","2":"two"},"escape":"\"\\\n"}`
	const want = `{"s":"\ud800\u0000é","m":{"2":"two","10":"ten"},"escape":"\"\\\n"}`
	value, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Marshal(value)
	if err != nil || string(first) != want {
		t.Fatalf("Marshal = %q, %v", first, err)
	}
	second, err := Marshal(value)
	if err != nil || string(second) != want {
		t.Fatalf("second Marshal = %q, %v", second, err)
	}
	text, err := Stringify(value)
	if err != nil || text != want {
		t.Fatalf("Stringify = %q, %v", text, err)
	}
	first[0] = '['
	if string(second) != want || text != want {
		t.Fatal("changing encoded bytes changed an independent output")
	}
}

func TestMarshalAndStringifyDiscardPartialOutput(t *testing.T) {
	cycle := make([]any, 1)
	cycle[0] = cycle
	for _, tc := range []struct {
		value any
		err   error
	}{
		{Undefined, ErrUndefinedRoot},
		{[]any{"prefix", "\xff"}, ErrStringEncoding},
		{[]any{"prefix", cycle}, ErrCycle},
		{[]any{"prefix", 1}, ErrUnsupportedType},
	} {
		b, err := Marshal(tc.value)
		if b != nil || !errors.Is(err, tc.err) {
			t.Fatalf("Marshal = %q, %v; want nil and %v", b, err, tc.err)
		}
		s, err := Stringify(tc.value)
		if s != "" || !errors.Is(err, tc.err) {
			t.Fatalf("Stringify = %q, %v; want empty and %v", s, err, tc.err)
		}
	}
}
