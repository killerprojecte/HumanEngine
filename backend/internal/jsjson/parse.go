package jsjson

import (
	"errors"
	"fmt"
	"strconv"
)

// SyntaxError identifies a byte offset (zero-based) in the supplied text.
// Its wording and offsets are not intended to match engine-specific JS errors.
type SyntaxError struct {
	Offset int
	Reason string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("jsjson: %s at byte %d", e.Reason, e.Offset)
}

// Parse implements JSON.parse for JSON values, without reviver/coercion.
// Its string input uses WTF-8, so even raw lone surrogates in JSON
// source text are representable. JSON files/wire bytes should normally contain
// UTF-8 with escaped lone surrogates. Returned strings and keys are canonical
// WTF-8, numbers are float64, arrays are []any, and objects are *Object.
// Duplicate object keys keep the last value and the first insertion position.
func Parse(text string) (any, error) {
	p := parser{text: text}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(text) {
		return nil, p.fail("unexpected trailing input")
	}
	return v, nil
}

// ParseBytes is Parse for bytes. It has the same explicit WTF-8 input contract;
// it does not silently replace malformed bytes as a UTF-8 text decoder might.
func ParseBytes(text []byte) (any, error) { return Parse(string(text)) }

type parser struct {
	text string
	pos  int
}

func (p *parser) fail(reason string) error { return &SyntaxError{Offset: p.pos, Reason: reason} }

func (p *parser) space() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) take(c byte) bool {
	if p.pos < len(p.text) && p.text[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *parser) value() (any, error) {
	p.space()
	if p.pos == len(p.text) {
		return nil, p.fail("expected a value")
	}
	switch p.text[p.pos] {
	case '"':
		return p.string()
	case '{':
		return p.object()
	case '[':
		return p.array()
	case 't':
		return p.literal("true", true)
	case 'f':
		return p.literal("false", false)
	case 'n':
		return p.literal("null", nil)
	default:
		return p.number()
	}
}

func (p *parser) literal(text string, value any) (any, error) {
	if len(p.text)-p.pos < len(text) || p.text[p.pos:p.pos+len(text)] != text {
		return nil, p.fail("invalid literal")
	}
	p.pos += len(text)
	return value, nil
}

func (p *parser) array() (any, error) {
	p.pos++
	a := make([]any, 0)
	p.space()
	if p.take(']') {
		return a, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.space()
		if p.take(']') {
			return a, nil
		}
		if !p.take(',') {
			return nil, p.fail("expected ',' or ']'")
		}
	}
}

func (p *parser) object() (any, error) {
	p.pos++
	o := new(Object)
	p.space()
	if p.take('}') {
		return o, nil
	}
	for {
		p.space()
		if p.pos == len(p.text) || p.text[p.pos] != '"' {
			return nil, p.fail("expected a property name")
		}
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		p.space()
		if !p.take(':') {
			return nil, p.fail("expected ':'")
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.set(key, v)
		p.space()
		if p.take('}') {
			return o, nil
		}
		if !p.take(',') {
			return nil, p.fail("expected ',' or '}'")
		}
	}
}

func (p *parser) string() (string, error) {
	p.pos++ // opening quote, checked by caller
	units := make([]uint16, 0)
	for p.pos < len(p.text) {
		c := p.text[p.pos]
		if c == '"' {
			p.pos++
			return FromUTF16(units), nil
		}
		if c < 0x20 {
			return "", p.fail("unescaped control character")
		}
		if c == '\\' {
			p.pos++
			if p.pos == len(p.text) {
				return "", p.fail("unterminated escape")
			}
			c = p.text[p.pos]
			p.pos++
			switch c {
			case '"', '\\', '/':
				units = append(units, uint16(c))
			case 'b':
				units = append(units, '\b')
			case 'f':
				units = append(units, '\f')
			case 'n':
				units = append(units, '\n')
			case 'r':
				units = append(units, '\r')
			case 't':
				units = append(units, '\t')
			case 'u':
				if len(p.text)-p.pos < 4 {
					return "", p.fail("incomplete unicode escape")
				}
				var unit uint16
				for j := 0; j < 4; j++ {
					h, ok := hex(p.text[p.pos])
					if !ok {
						return "", p.fail("invalid unicode escape")
					}
					unit = unit*16 + uint16(h)
					p.pos++
				}
				units = append(units, unit)
			default:
				return "", p.fail("invalid escape")
			}
			continue
		}
		r, n := decodeRune(p.text[p.pos:])
		if n == 0 {
			return "", p.fail("invalid WTF-8")
		}
		p.pos += n
		if r <= 0xffff {
			units = append(units, uint16(r))
		} else {
			r -= 0x10000
			units = append(units, uint16(0xd800+r/0x400), uint16(0xdc00+r%0x400))
		}
	}
	return "", p.fail("unterminated string")
}

func hex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

func (p *parser) number() (any, error) {
	start := p.pos
	p.take('-')
	if p.pos == len(p.text) {
		return nil, p.fail("invalid number")
	}
	if p.take('0') {
		// The enclosing grammar rejects a subsequent digit.
	} else if p.text[p.pos] >= '1' && p.text[p.pos] <= '9' {
		for p.pos < len(p.text) && digit(p.text[p.pos]) {
			p.pos++
		}
	} else {
		return nil, p.fail("invalid number")
	}
	if p.take('.') {
		if p.pos == len(p.text) || !digit(p.text[p.pos]) {
			return nil, p.fail("expected fraction digits")
		}
		for p.pos < len(p.text) && digit(p.text[p.pos]) {
			p.pos++
		}
	}
	if p.take('e') || p.take('E') {
		if !p.take('+') {
			p.take('-')
		}
		if p.pos == len(p.text) || !digit(p.text[p.pos]) {
			return nil, p.fail("expected exponent digits")
		}
		for p.pos < len(p.text) && digit(p.text[p.pos]) {
			p.pos++
		}
	}
	f, err := strconv.ParseFloat(p.text[start:p.pos], 64)
	// JSON.parse accepts overflow as Infinity and preserves underflow's sign.
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, p.fail("invalid number")
	}
	return f, nil
}
