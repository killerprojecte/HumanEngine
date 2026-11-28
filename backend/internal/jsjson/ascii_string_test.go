package jsjson

import (
	"strings"
	"testing"
)

func TestASCIIStringPrefix(t *testing.T) {
	prefix := strings.Repeat("AbC0/a+1", 1024)
	for _, tc := range []struct{ input, value, wire string }{
		{`"` + prefix + `"`, prefix, `"` + prefix + `"`},
		{`"` + prefix + `\n\"\\\/"`, prefix + "\n\"\\/", `"` + prefix + `\n\"\\/"`},
		{`"` + prefix + `\ud83d\ude42\ud800é"`, prefix + "🙂\xed\xa0\x80é", `"` + prefix + `🙂\ud800é"`},
	} {
		value, err := Parse(tc.input)
		if err != nil || value != tc.value {
			t.Fatalf("prefix decode mismatch: %v", err)
		}
		checkEncoding(t, value, tc.wire)
	}
	for _, suffix := range []string{"", "\n\"", "\xff\"", `\q"`} {
		if _, err := Parse(`"` + prefix + suffix); err == nil {
			t.Fatalf("invalid suffix %q accepted", suffix)
		}
	}
}

func BenchmarkASCIIString(b *testing.B) {
	for _, tc := range []struct{ name, value string }{
		{"text", "Synthetic receive 43."},
		{"inline-384KiB", strings.Repeat("AbC0/a+1", 48*1024)},
		{"mixed-text", "Synthetic 猫 🙂 with a \"quote\""},
	} {
		b.Run(tc.name+"/parse", func(b *testing.B) {
			wire, err := Stringify(tc.value)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(wire)))
			for b.Loop() {
				_, _ = Parse(wire)
			}
		})
		b.Run(tc.name+"/marshal", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.value)))
			for b.Loop() {
				_, _ = Marshal(tc.value)
			}
		})
	}
}
