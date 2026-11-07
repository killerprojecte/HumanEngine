package jsjson

import (
	hexencoding "encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

type observation struct {
	Kind        string           `json:"kind"`
	Bits        string           `json:"bits,omitempty"`
	Boolean     bool             `json:"boolean,omitempty"`
	Units       []uint16         `json:"units,omitempty"`
	UTF16Length int              `json:"utf16Length,omitempty"`
	CodePoints  int              `json:"codePoints,omitempty"`
	Values      []observation    `json:"values,omitempty"`
	Members     []observedMember `json:"members,omitempty"`
}

type observedMember struct {
	Units []uint16    `json:"units"`
	Value observation `json:"value"`
}

type oracle struct {
	Node       string `json:"node"`
	ParseCases []struct {
		Name        string      `json:"name"`
		SourceUnits []uint16    `json:"sourceUnits"`
		Valid       bool        `json:"valid"`
		Encoded     string      `json:"encoded"`
		Value       observation `json:"value"`
	} `json:"parseCases"`
	StringCases []struct {
		Name        string   `json:"name"`
		Units       []uint16 `json:"units"`
		Encoded     string   `json:"encoded"`
		UTF16Length int      `json:"utf16Length"`
		CodePoints  int      `json:"codePoints"`
	} `json:"stringCases"`
	NumberCases []struct {
		Bits    string `json:"bits"`
		Encoded string `json:"encoded"`
	} `json:"numberCases"`
	ObjectCases []struct {
		Name       string `json:"name"`
		Operations []struct {
			Kind   string   `json:"kind"`
			Units  []uint16 `json:"units"`
			Source string   `json:"source"`
		} `json:"operations"`
		Encoded string      `json:"encoded"`
		Value   observation `json:"value"`
	} `json:"objectCases"`
}

func loadOracle(t *testing.T) oracle {
	t.Helper()
	data, err := os.ReadFile("testdata/node24.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture oracle
	// Fixture strings containing actual lone surrogates are represented by
	// uint16 arrays. Using encoding/json for the fixture container is safe.
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Node != "v24.22.1" {
		t.Fatalf("unexpected oracle %q", fixture.Node)
	}
	return fixture
}

func observe(t *testing.T, value any) observation {
	t.Helper()
	switch v := value.(type) {
	case nil:
		return observation{Kind: "null"}
	case bool:
		return observation{Kind: "boolean", Boolean: v}
	case float64:
		bits := strconv.FormatUint(math.Float64bits(v), 16)
		return observation{Kind: "number", Bits: strings.Repeat("0", 16-len(bits)) + bits}
	case string:
		units, err := UTF16(v)
		if err != nil {
			t.Fatal(err)
		}
		length, err := UTF16Len(v)
		if err != nil {
			t.Fatal(err)
		}
		points, err := CodePointCount(v)
		if err != nil {
			t.Fatal(err)
		}
		return observation{Kind: "string", Units: units, UTF16Length: length, CodePoints: points}
	case []any:
		values := make([]observation, len(v))
		for i, item := range v {
			values[i] = observe(t, item)
		}
		return observation{Kind: "array", Values: values}
	case *Object:
		members := make([]observedMember, 0, v.Len())
		for _, member := range v.Members() {
			units, err := UTF16(member.Key)
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, observedMember{Units: units, Value: observe(t, member.Value)})
		}
		return observation{Kind: "object", Members: members}
	default:
		t.Fatalf("unexpected type %T", value)
		return observation{}
	}
}

func checkEncoding(t *testing.T, value any, want string) {
	t.Helper()
	actual, err := Stringify(value)
	if err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("encoding mismatch:\nactual %q\nwant   %q", actual, want)
	}
	if !utf8.ValidString(actual) {
		t.Fatal("JSON output must always be valid UTF-8")
	}
	encoded, err := Marshal(value)
	if err != nil || string(encoded) != actual {
		t.Fatalf("Marshal disagrees: %q, %v", encoded, err)
	}
}

func TestNode24ParseFixtures(t *testing.T) {
	fixture := loadOracle(t)
	for _, tc := range fixture.ParseCases {
		t.Run(tc.Name, func(t *testing.T) {
			source := FromUTF16(tc.SourceUnits)
			value, err := Parse(source)
			if !tc.Valid {
				var syntax *SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatalf("invalid JSON accepted: %#v, %v", value, err)
				}
				if syntax.Offset < 0 || syntax.Offset > len(source) {
					t.Fatalf("invalid error offset: %v", syntax)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := observe(t, value); !reflect.DeepEqual(got, tc.Value) {
				t.Fatalf("parsed value mismatch:\nactual %#v\nwant   %#v", got, tc.Value)
			}
			checkEncoding(t, value, tc.Encoded)
			fromBytes, err := ParseBytes([]byte(source))
			if err != nil || !reflect.DeepEqual(observe(t, fromBytes), tc.Value) {
				t.Fatalf("ParseBytes disagrees: %v", err)
			}
			// Serialization stabilizes after the first pass, even for -0 and
			// overflow (whose JSON.stringify forms are 0 and null).
			roundtrip, err := Parse(tc.Encoded)
			if err != nil {
				t.Fatal(err)
			}
			checkEncoding(t, roundtrip, tc.Encoded)
		})
	}
}

func TestNode24StringFixtures(t *testing.T) {
	fixture := loadOracle(t)
	for _, tc := range fixture.StringCases {
		t.Run(tc.Name, func(t *testing.T) {
			s := FromUTF16(tc.Units)
			got, err := UTF16(s)
			if err != nil || !reflect.DeepEqual(got, tc.Units) {
				t.Fatalf("UTF16 roundtrip failed: %v", err)
			}
			length, err := UTF16Len(s)
			if err != nil || length != tc.UTF16Length {
				t.Fatalf("UTF16 length %d, want %d: %v", length, tc.UTF16Length, err)
			}
			points, err := CodePointCount(s)
			if err != nil || points != tc.CodePoints {
				t.Fatalf("code point count %d, want %d: %v", points, tc.CodePoints, err)
			}
			checkEncoding(t, s, tc.Encoded)
			parsed, err := Parse(tc.Encoded)
			if err != nil || parsed != s {
				t.Fatalf("JSON string roundtrip failed: %v", err)
			}
		})
	}
}

func TestNode24NumberFixtures(t *testing.T) {
	fixture := loadOracle(t)
	for _, tc := range fixture.NumberCases {
		t.Run(tc.Bits, func(t *testing.T) {
			bits, err := strconv.ParseUint(tc.Bits, 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			f := math.Float64frombits(bits)
			checkEncoding(t, f, tc.Encoded)
			parsed, err := Parse(tc.Encoded)
			if err != nil {
				t.Fatal(err)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				if parsed != nil {
					t.Fatalf("nonfinite number should stringify to null: %v", parsed)
				}
			} else {
				want := bits
				if f == 0 {
					want = 0 // JSON.stringify normalizes negative zero.
				}
				if got := math.Float64bits(parsed.(float64)); got != want {
					t.Fatalf("decimal roundtrip bits %016x, want %016x", got, want)
				}
			}
		})
	}
}

func TestNode24ObjectOperations(t *testing.T) {
	fixture := loadOracle(t)
	for _, tc := range fixture.ObjectCases {
		t.Run(tc.Name, func(t *testing.T) {
			o := new(Object)
			for _, op := range tc.Operations {
				key := FromUTF16(op.Units)
				if op.Kind == "delete" {
					o.Delete(key)
					continue
				}
				value, err := Parse(op.Source)
				if err != nil {
					t.Fatal(err)
				}
				if err := o.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			if got := observe(t, o); !reflect.DeepEqual(got, tc.Value) {
				t.Fatalf("object mismatch:\nactual %#v\nwant   %#v", got, tc.Value)
			}
			checkEncoding(t, o, tc.Encoded)
		})
	}
}

func TestLoneSurrogateIdentityAndCopy(t *testing.T) {
	value, err := Parse(`{"\ud800":1,"\ud801":2,"�":3,"😀":4,"\ud83d\ude00":5}`)
	if err != nil {
		t.Fatal(err)
	}
	o := value.(*Object)
	if o.Len() != 4 {
		t.Fatalf("distinct surrogate identities collapsed: %d", o.Len())
	}
	for _, tc := range []struct {
		key  string
		want float64
	}{{FromUTF16([]uint16{0xd800}), 1}, {FromUTF16([]uint16{0xd801}), 2}, {"�", 3}, {"😀", 5}} {
		if v, ok := o.Get(tc.key); !ok || v != tc.want {
			t.Fatalf("Get(%x): %#v, %v", tc.key, v, ok)
		}
	}
	copy := o.Clone()
	if err := copy.Set("new", true); err != nil {
		t.Fatal(err)
	}
	copy.Delete("😀")
	if _, ok := o.Get("new"); ok {
		t.Fatal("clone shares property storage")
	}
	if _, ok := o.Get("😀"); !ok {
		t.Fatal("clone delete affected original")
	}
	members := o.Members()
	members[0] = Member{Key: "changed", Value: false}
	if o.Keys()[0] == "changed" {
		t.Fatal("Members exposes internal storage")
	}
	child := new(Object)
	if err := o.Set("child", child); err != nil {
		t.Fatal(err)
	}
	if value, _ := o.Clone().Get("child"); value != child {
		t.Fatal("Clone must be explicitly shallow")
	}
}

func TestUnsupportedValuesAndCycles(t *testing.T) {
	for _, value := range []any{map[string]any{"x": true}, struct{ X bool }{true}, 1, int64(1), float32(1), []string{"x"}, (*Object)(nil), make(chan int), func() {}} {
		if _, err := Stringify(value); !errors.Is(err, ErrUnsupportedType) {
			t.Fatalf("%T: want unsupported error, got %v", value, err)
		}
	}
	o := new(Object)
	if err := o.Set("self", o); err != nil {
		t.Fatal(err)
	}
	a := make([]any, 1)
	a[0] = a
	for _, value := range []any{o, a, []any{o}} {
		if _, err := Stringify(value); !errors.Is(err, ErrCycle) {
			t.Fatalf("want cycle error, got %v", err)
		}
	}
	child := new(Object)
	checkEncoding(t, []any{child, child}, `[{},{}]`)
	checkEncoding(t, []any{[]any{}, []any{}}, `[[],[]]`)
	checkEncoding(t, []any(nil), `[]`)
	// Overlapping slice headers are not necessarily cyclic. Include their
	// length in path identity, rather than rejecting any shared backing array.
	b := make([]any, 2)
	b[1] = b[:1]
	checkEncoding(t, b, `[null,[null]]`)
}

func TestInvalidStringEncoding(t *testing.T) {
	for _, encoded := range []string{
		"ff", "80", "c080", "eda0", "f4908080", "f0808080", "eda080edb080",
	} {
		b, err := hexencoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if err := ValidateString(s); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: invalid encoding accepted", encoded)
		}
		if _, err := Stringify(s); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: encoder accepted invalid string", encoded)
		}
		if _, err := UTF16(s); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: UTF16 accepted invalid string", encoded)
		}
		if _, err := UTF16Len(s); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: UTF16Len accepted invalid string", encoded)
		}
		if _, err := CodePointCount(s); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: CodePointCount accepted invalid string", encoded)
		}
		if _, err := NewObject(Member{Key: s}); !errors.Is(err, ErrStringEncoding) {
			t.Fatalf("%s: object accepted invalid key", encoded)
		}
	}
	for _, s := range []string{"\xff", "\x80", "\xc0\x80", "\xed\xa0", "\xf4\x90\x80\x80"} {
		if _, err := Parse(`"` + s + `"`); err == nil {
			t.Fatalf("malformed source accepted: %x", s)
		}
	}
}

func TestNoArbitraryDepthOrStringLimit(t *testing.T) {
	// Explicitly exceed encoding/json's fixed 10,000-level parse limit.
	const depth = 12000
	source := strings.Repeat("[", depth) + `"` + strings.Repeat("a", 100001) + `"` + strings.Repeat("]", depth)
	value, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	checkEncoding(t, value, source)
}

func FuzzParseStringify(f *testing.F) {
	for _, s := range []string{`null`, `{"\ud800":[-0,1e400,"😀"]}`, `{"2":0,"1":1,"b":2,"b":3}`, `"\ud800\ud801\udc00"`, `"\u0000"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, source string) {
		value, err := Parse(source)
		if err != nil {
			return
		}
		encoded, err := Stringify(value)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(encoded) {
			t.Fatal("invalid UTF-8 output")
		}
		again, err := Parse(encoded)
		if err != nil {
			t.Fatal(err)
		}
		second, err := Stringify(again)
		if err != nil || second != encoded {
			t.Fatalf("unstable roundtrip: %q / %q: %v", encoded, second, err)
		}
	})
}
