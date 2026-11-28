package snowluma

import (
	"errors"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestMediaReferenceCodeUnitsAndCase(t *testing.T) {
	for _, tt := range []struct {
		label, text string
		want        error
	}{
		{"ordinary limit", strings.Repeat("x", 16384), nil},
		{"ordinary over", strings.Repeat("x", 16385), protocolError("invalid_media_reference")},
		{"astral limit", strings.Repeat("x", 16382) + "𐐀", nil},
		{"astral over", strings.Repeat("x", 16383) + "𐐀", protocolError("invalid_media_reference")},
		{"lone surrogate", strings.Repeat("x", 16383) + jsjson.FromUTF16([]uint16{0xd800}), nil},
		{"malformed", "base64://x\xff", jsjson.ErrStringEncoding},
		{"uppercase inline", "BASE64://" + strings.Repeat("AbC0", 4097), nil},
		{"mixed data URI", "Data:IMAGE/Png;base64," + strings.Repeat("AbC0", 4097), nil},
		{"legacy unicode lowercase", "data:İmage/png;base64," + strings.Repeat("AbC0", 4097), nil},
	} {
		t.Run(tt.label, func(t *testing.T) {
			got, err := mediaReference(tt.text)
			if !errors.Is(err, tt.want) || err == nil && got != tt.text {
				t.Fatalf("wrong result: %v", err)
			}
		})
	}
}

func BenchmarkMediaReference(b *testing.B) {
	for _, input := range []struct{ name, text string }{
		{"opaque", " fixture-token "},
		{"inline-384KiB", "base64://" + strings.Repeat("AbC0/a+1", 48*1024)},
	} {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input.text)))
			for b.Loop() {
				if _, err := mediaReference(input.text); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
