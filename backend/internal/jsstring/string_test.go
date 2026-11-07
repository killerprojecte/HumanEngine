package jsstring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type nodeOracle struct {
	Node       string `json:"node"`
	ICU        string `json:"icu"`
	Unicode    string `json:"unicode"`
	Exhaustive struct {
		CodePoints        int    `json:"codePoints"`
		LowerSHA256       string `json:"lowerSHA256"`
		FingerprintSHA256 string `json:"fingerprintSHA256"`
	} `json:"exhaustive"`
	Cases []struct {
		Name        string   `json:"name"`
		Input       []uint16 `json:"input"`
		Lower       []uint16 `json:"lower"`
		Fingerprint []uint16 `json:"fingerprint"`
	} `json:"cases"`
}

func loadOracle(t *testing.T) nodeOracle {
	t.Helper()
	data, err := os.ReadFile("testdata/node24.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle nodeOracle
	// All JavaScript strings are uint16 arrays: encoding/json cannot replace
	// unpaired surrogate code units at this test-container boundary.
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Node != "v24.22.1" || oracle.ICU != "78.3" || oracle.Unicode != "17.0" {
		t.Fatalf("unexpected oracle: %s / ICU %s / Unicode %s", oracle.Node, oracle.ICU, oracle.Unicode)
	}
	return oracle
}

func TestNode24StringOperations(t *testing.T) {
	for _, tc := range loadOracle(t).Cases {
		t.Run(tc.Name, func(t *testing.T) {
			input := jsjson.FromUTF16(tc.Input)
			for _, operation := range []struct {
				name string
				call func(string) (string, error)
				want []uint16
			}{
				{"lower", Lower, tc.Lower},
				{"fingerprint", FingerprintText, tc.Fingerprint},
			} {
				actual, err := operation.call(input)
				if err != nil {
					t.Fatalf("%s: %v", operation.name, err)
				}
				if want := jsjson.FromUTF16(operation.want); actual != want {
					t.Fatalf("%s mismatch: got %q, want %q", operation.name, actual, want)
				}
				if err := jsjson.ValidateString(actual); err != nil {
					t.Fatalf("%s produced noncanonical WTF-8: %v", operation.name, err)
				}
			}
		})
	}
}

func TestNode24EveryCodePoint(t *testing.T) {
	oracle := loadOracle(t)
	if oracle.Exhaustive.CodePoints != 0x110000 {
		t.Fatalf("oracle does not cover the entire codespace: %d", oracle.Exhaustive.CodePoints)
	}
	var units []uint16
	for point := rune(0); point <= 0x10ffff; point++ {
		units = appendPoint(units, point)
		units = append(units, 0) // Matches the Node generator's context separator.
	}
	input := jsjson.FromUTF16(units)
	for _, operation := range []struct {
		name string
		call func(string) (string, error)
		want string
	}{
		{"lower", Lower, oracle.Exhaustive.LowerSHA256},
		{"fingerprint", FingerprintText, oracle.Exhaustive.FingerprintSHA256},
	} {
		t.Run(operation.name, func(t *testing.T) {
			output, err := operation.call(input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := jsjson.Stringify(output)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(encoded))
			if got := hex.EncodeToString(digest[:]); got != operation.want {
				t.Fatalf("exhaustive Node digest mismatch: got %s, want %s", got, operation.want)
			}
		})
	}
}

func TestRejectsInvalidStringEncoding(t *testing.T) {
	for _, input := range []string{
		"\xff", "\xc0\x80", "\xe0\x80\x80", "\xf4\x90\x80\x80",
		"\xed\xa0", "\xed\xa0\x80\xed\xb0\x80", "ok\x80",
	} {
		for _, operation := range []func(string) (string, error){Lower, FingerprintText} {
			if output, err := operation(input); !errors.Is(err, jsjson.ErrStringEncoding) || output != "" {
				t.Fatalf("invalid input %x: output=%q err=%v", input, output, err)
			}
		}
	}
}

func TestGeneratedTables(t *testing.T) {
	for i, mapping := range lowercaseMappings {
		if mapping.point < 0 || mapping.point > 0x10ffff || (i > 0 && lowercaseMappings[i-1].point >= mapping.point) {
			t.Fatalf("invalid or unsorted lowercase mapping at %d", i)
		}
		if len(mapping.units) == 0 {
			t.Fatalf("root lowercase unexpectedly deletes U+%04X", mapping.point)
		}
	}
	for _, ranges := range [][]codePointRange{casedRanges[:], caseIgnorableRanges[:], whitespaceRanges[:]} {
		for i, entry := range ranges {
			if entry.first < 0 || entry.first > entry.last || entry.last > 0x10ffff || (i > 0 && ranges[i-1].last+1 >= entry.first) {
				t.Fatalf("invalid or unmerged property range at %d: %+v", i, entry)
			}
		}
	}
	if !inRanges(0xfeff, whitespaceRanges[:]) || inRanges(0x85, whitespaceRanges[:]) || inRanges(0x180e, whitespaceRanges[:]) {
		t.Fatal("ECMAScript whitespace differs from the pinned baseline")
	}
}
