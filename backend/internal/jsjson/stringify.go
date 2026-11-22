// Package jsjson preserves JSON.parse / JSON.stringify data semantics,
// including lone UTF-16 surrogates, binary64 numbers, and property order.
// See README.md for the supported data model and migration boundary.
package jsjson

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

var (
	ErrUnsupportedType = errors.New("jsjson: unsupported value type")
	ErrCycle           = errors.New("jsjson: cyclic value")
)

// Stringify encodes nil, bool, float64, canonical WTF-8 string, []any, and
// *Object, recursively, plus the dedicated Undefined value. It implements compact JSON.stringify on that data
// model, including nonfinite numbers becoming null and negative zero becoming
// 0. Go maps/structs/integers are intentionally unsupported: they cannot imply
// JS insertion order, property presence, or Number conversion semantics.
// Cycles are errors; repeated noncyclic references are allowed.
func Stringify(value any) (string, error) {
	if IsUndefined(value) {
		return "", ErrUndefinedRoot
	}
	e := encoder{active: make(map[reference]bool)}
	if err := e.value(value); err != nil {
		return "", err
	}
	return string(e.out), nil
}

func Marshal(value any) ([]byte, error) {
	s, err := Stringify(value)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

type reference struct {
	kind    byte
	pointer uintptr
	length  int
}

type encoder struct {
	out    []byte
	active map[reference]bool
}

func (e *encoder) enter(ref reference) error {
	if e.active[ref] {
		return ErrCycle
	}
	e.active[ref] = true
	return nil
}

func (e *encoder) value(value any) error {
	switch v := value.(type) {
	case nil:
		e.out = append(e.out, "null"...)
	case bool:
		e.out = strconv.AppendBool(e.out, v)
	case float64:
		e.out = append(e.out, numberString(v)...)
	case string:
		return e.string(v)
	case []any:
		ref := reference{kind: '[', pointer: reflect.ValueOf(v).Pointer(), length: len(v)}
		if err := e.enter(ref); err != nil {
			return err
		}
		defer delete(e.active, ref)
		e.out = append(e.out, '[')
		for i, item := range v {
			if i > 0 {
				e.out = append(e.out, ',')
			}
			if IsUndefined(item) {
				e.out = append(e.out, "null"...)
				continue
			}
			if err := e.value(item); err != nil {
				return err
			}
		}
		e.out = append(e.out, ']')
	case *Object:
		if v == nil {
			return fmt.Errorf("%w: nil *Object", ErrUnsupportedType)
		}
		ref := reference{kind: '{', pointer: reflect.ValueOf(v).Pointer()}
		if err := e.enter(ref); err != nil {
			return err
		}
		defer delete(e.active, ref)
		e.out = append(e.out, '{')
		written := false
		for _, member := range v.Members() {
			if IsUndefined(member.Value) {
				continue
			}
			if written {
				e.out = append(e.out, ',')
			}
			written = true
			if err := e.string(member.Key); err != nil {
				return err
			}
			e.out = append(e.out, ':')
			if err := e.value(member.Value); err != nil {
				return err
			}
		}
		e.out = append(e.out, '}')
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedType, value)
	}
	return nil
}

func (e *encoder) string(s string) error {
	if err := ValidateString(s); err != nil {
		return err
	}
	e.out = append(e.out, '"')
	for i := 0; i < len(s); {
		r, n := decodeRune(s[i:])
		i += n
		switch r {
		case '"', '\\':
			e.out = append(e.out, '\\', byte(r))
		case '\b':
			e.out = append(e.out, `\b`...)
		case '\f':
			e.out = append(e.out, `\f`...)
		case '\n':
			e.out = append(e.out, `\n`...)
		case '\r':
			e.out = append(e.out, `\r`...)
		case '\t':
			e.out = append(e.out, `\t`...)
		default:
			if r < 0x20 || high(r) || low(r) {
				const digits = "0123456789abcdef"
				e.out = append(e.out, '\\', 'u', digits[r>>12&15], digits[r>>8&15], digits[r>>4&15], digits[r&15])
			} else {
				e.out = append(e.out, s[i-n:i]...)
			}
		}
	}
	e.out = append(e.out, '"')
	return nil
}

func numberString(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	// Go's shortest, nearest, ties-to-even binary64 decimal digits agree with
	// ECMAScript Number::toString. Its presentation thresholds differ, so use
	// scientific digits and apply ECMAScript's fixed/exponent layout ourselves.
	s := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	i := strings.IndexByte(s, 'e')
	digits := strings.ReplaceAll(s[:i], ".", "")
	exponent, _ := strconv.Atoi(s[i+1:])
	if exponent >= -6 && exponent < 21 {
		point := exponent + 1
		if point <= 0 {
			return sign + "0." + strings.Repeat("0", -point) + digits
		}
		if point >= len(digits) {
			return sign + digits + strings.Repeat("0", point-len(digits))
		}
		return sign + digits[:point] + "." + digits[point:]
	}
	coefficient := digits[:1]
	if len(digits) > 1 {
		coefficient += "." + digits[1:]
	}
	exponentSign := ""
	if exponent >= 0 {
		exponentSign = "+"
	}
	return sign + coefficient + "e" + exponentSign + strconv.Itoa(exponent)
}
