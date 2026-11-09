package observability

import (
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math/big"
	"reflect"
	"sort"
	"strconv"
)

var ErrUnserializableLog = errors.New("log_record_unserializable")

type snapshotVisit struct {
	kind    reflect.Kind
	pointer uintptr
	length  int
}
type logSnapshotter struct{ active map[snapshotVisit]bool }

// Snapshot caller data once before retention/admission. Native JSON primitives
// preserve JS Number/WTF-8 semantics; an explicit Go JSON marshaler is invoked
// once for that occurrence and its returned JSON is parsed without re-calling it.
func snapshotLogFields(fields map[string]any) (result *jsjson.Object, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = ErrUnserializableLog
		}
	}()
	value, err := (&logSnapshotter{active: make(map[snapshotVisit]bool)}).value(fields)
	if err != nil {
		return nil, ErrUnserializableLog
	}
	result, _ = value.(*jsjson.Object)
	return result, nil
}
func (s *logSnapshotter) enter(v snapshotVisit) (func(), error) {
	if s.active[v] {
		return nil, ErrUnserializableLog
	}
	s.active[v] = true
	return func() { delete(s.active, v) }, nil
}
func (s *logSnapshotter) value(input any) (any, error) {
	if input == nil || jsjson.IsUndefined(input) {
		return input, nil
	}
	switch value := input.(type) {
	case string:
		if err := jsjson.ValidateString(value); err != nil {
			return nil, err
		}
		return value, nil
	case bool:
		return value, nil
	case float64:
		return value, nil
	case float32:
		return float64(value), nil
	case json.Number:
		return strconv.ParseFloat(string(value), 64)
	case big.Int, *big.Int:
		return nil, ErrUnserializableLog
	case *jsjson.Object:
		if value == nil {
			return nil, nil
		}
		leave, err := s.enter(snapshotVisit{kind: reflect.Pointer, pointer: reflect.ValueOf(value).Pointer()})
		if err != nil {
			return nil, err
		}
		defer leave()
		out, _ := jsjson.NewObject()
		for _, member := range value.Members() {
			v, err := s.value(member.Value)
			if err != nil {
				return nil, err
			}
			if err = out.Set(member.Key, v); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	rv := reflect.ValueOf(input)
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return nil, nil
	}
	if marshaler, ok := input.(json.Marshaler); ok {
		bytes, err := marshaler.MarshalJSON()
		if err != nil {
			return nil, err
		}
		return jsjson.ParseBytes(bytes)
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), nil
	case reflect.String:
		text := rv.String()
		if err := jsjson.ValidateString(text); err != nil {
			return nil, err
		}
		return text, nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Pointer:
		leave, err := s.enter(snapshotVisit{kind: reflect.Pointer, pointer: rv.Pointer()})
		if err != nil {
			return nil, err
		}
		defer leave()
		return s.value(rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, ErrUnserializableLog
		}
		leave, err := s.enter(snapshotVisit{kind: reflect.Map, pointer: uintptr(rv.UnsafePointer())})
		if err != nil {
			return nil, err
		}
		defer leave()
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		out, _ := jsjson.NewObject()
		for _, key := range keys {
			v, err := s.value(rv.MapIndex(key).Interface())
			if err != nil {
				return nil, err
			}
			if err = out.Set(key.String(), v); err != nil {
				return nil, err
			}
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice {
			leave, err := s.enter(snapshotVisit{kind: reflect.Slice, pointer: rv.Pointer(), length: rv.Len()})
			if err != nil {
				return nil, err
			}
			defer leave()
		}
		out := make([]any, rv.Len())
		for i := range out {
			value, err := s.value(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case reflect.Struct:
		// Struct tags and standard-library value types retain their explicit Go JSON
		// representation. They are not traversed a second time after this snapshot.
		bytes, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		return jsjson.ParseBytes(bytes)
	default:
		return nil, ErrUnserializableLog
	}
}
