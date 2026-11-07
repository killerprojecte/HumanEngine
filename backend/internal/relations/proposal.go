package relations

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"regexp"
	"strconv"
)

var ErrSchema = errors.New("invalid_relation_proposal")

type decoder func(any) (any, error)
type field struct {
	name     string
	decode   decoder
	optional bool
}

func f(name string, d decoder) field   { return field{name, d, false} }
func opt(name string, d decoder) field { return field{name, d, true} }
func shape(v any, fields ...field) (*Object, error) {
	o := object(v)
	if o == nil {
		return nil, ErrSchema
	}
	keys := map[string]bool{}
	out := new(Object)
	for _, f := range fields {
		keys[f.name] = true
		x, present := o.Get(f.name)
		if !present {
			if f.optional {
				continue
			}
			return nil, ErrSchema
		}
		x, e := f.decode(x)
		if e != nil {
			return nil, e
		}
		_ = out.Set(f.name, x)
	}
	for _, k := range o.Keys() {
		if !keys[k] {
			return nil, ErrSchema
		}
	}
	return out, nil
}
func bounded(min, max int) decoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, ErrSchema
		}
		n, e := jsjson.CodePointCount(s)
		if e != nil || n < min || n > max {
			return nil, ErrSchema
		}
		return s, nil
	}
}

var idValue = bounded(1, 512)

func safe(min float64) decoder {
	return func(v any) (any, error) {
		n, ok := v.(float64)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < min || n > 9007199254740991 || math.Trunc(n) != n {
			return nil, ErrSchema
		}
		return n, nil
	}
}
func enumeration(values ...string) decoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if ok {
			for _, candidate := range values {
				if s == candidate {
					return s, nil
				}
			}
		}
		return nil, ErrSchema
	}
}
func list(d decoder, min, max int) decoder {
	return func(v any) (any, error) {
		a, ok := v.([]any)
		if !ok || len(a) < min || len(a) > max {
			return nil, ErrSchema
		}
		out := make([]any, len(a))
		for i, x := range a {
			n, e := d(x)
			if e != nil {
				return nil, e
			}
			out[i] = n
		}
		return out, nil
	}
}

var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func instant(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !instantPattern.MatchString(s) {
		return nil, ErrSchema
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:7])
	day, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || day < 1 {
		return nil, ErrSchema
	}
	days := [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days[1] = 29
	}
	if day > days[m-1] {
		return nil, ErrSchema
	}
	return s, nil
}

var utcDate, _ = jsdate.New("UTC")

func parseProposal(v any) (*Object, error) {
	target := func(v any) (any, error) {
		switch get(v, "kind") {
		case "new":
			return shape(v, f("kind", enumeration("new")), f("entityKind", enumeration("plan", "event")), f("title", bounded(1, 500)))
		case "existing":
			return shape(v, f("kind", enumeration("existing")), f("entityId", idValue))
		}
		return nil, ErrSchema
	}
	o, e := shape(v, f("predicate", enumeration("participates_in")), f("modality", enumeration("asserted", "possible")), f("temporalScope", enumeration("current", "past", "future", "unspecified")), f("personId", idValue), f("target", target), f("polarity", enumeration("positive", "negative")), f("conditions", list(bounded(1, 2000), 0, 8)), opt("occurredAt", instant), opt("validFrom", instant), opt("validUntil", instant))
	if e != nil {
		return nil, e
	}
	if has(o, "validFrom") && has(o, "validUntil") {
		a, _, _ := utcDate.ParseMillis(str(get(o, "validFrom")))
		b, _, _ := utcDate.ParseMillis(str(get(o, "validUntil")))
		if a > b {
			return nil, ErrSchema
		}
	}
	return o, nil
}
