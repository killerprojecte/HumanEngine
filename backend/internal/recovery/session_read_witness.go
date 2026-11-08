package recovery

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"strings"
)

type readWitnessQuery struct {
	SQL  string
	Args []any
}
type readWitnessResult struct {
	Columns []string
	Rows    [][]any
}

// ReadWitness is an immutable post-operation snapshot of the exact SELECTs
// actually issued through one owned session, including its nested savepoints.
// It intentionally does not enumerate unrelated tables, scenes, or identities.
type ReadWitness struct {
	queries []readWitnessQuery
	results []readWitnessResult
}

var ErrReadWitnessChanged = errors.New("recovery_read_witness_changed")

func (s *WriteSession) captureRead(query string, args []any) error {
	if s.readCapture == nil {
		return nil
	}
	// Do not replay arbitrary SQL, RETURNING writes, PRAGMAs, or CTE mutations.
	// Current domain read ports all issue SELECT. Unsupported future reads fail
	// closed and require an explicit extension of this capability.
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT ") || strings.Contains(query, ";") {
		return errors.New("recovery_read_witness_select_required")
	}
	copyArgs := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case []byte:
			copyArgs[i] = append([]byte(nil), v...)
		case nil, string, int, int64, float64, bool:
			copyArgs[i] = v
		default:
			return errors.New("recovery_read_witness_argument_invalid")
		}
	}
	*s.readCapture = append(*s.readCapture, readWitnessQuery{query, copyArgs})
	return nil
}

type witnessRows interface {
	Next() bool
	Scan(...any) error
	Columns() ([]string, error)
	Err() error
	Close() error
}

func witnessResult(rows witnessRows) (out readWitnessResult, err error) {
	defer func() { err = errors.Join(err, rows.Close()) }()
	out.Columns, err = rows.Columns()
	if err != nil {
		return
	}
	out.Rows = [][]any{}
	for rows.Next() {
		values := make([]any, len(out.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = append([]byte(nil), b...)
			}
		}
		out.Rows = append(out.Rows, values)
	}
	err = rows.Err()
	return
}

// CaptureReadWitness must run inside the active owner callback. fn may join
// nested sessions but cannot escape the lease. Captured reads are materialized
// after fn succeeds, so committed mutations are never replayed at publication.
func (s *WriteSession) CaptureReadWitness(fn func() error) (w *ReadWitness, err error) {
	s.mu.Lock()
	if err = s.check(); err != nil {
		s.mu.Unlock()
		return
	}
	if fn == nil || s.readCapture != nil {
		s.mu.Unlock()
		return nil, errors.New("recovery_read_witness_scope_invalid")
	}
	queries := []readWitnessQuery{}
	s.readCapture = &queries
	s.mu.Unlock()
	func() { defer func() { s.mu.Lock(); s.readCapture = nil; s.mu.Unlock() }(); err = fn() }()
	if err != nil {
		return nil, err
	}
	// The gate participates even if all domain reads return empty rows.
	queries = append(queries, readWitnessQuery{SQL: "SELECT value FROM recovery_gate WHERE id=1"})
	w = &ReadWitness{}
	seen := map[string]bool{}
	for _, q := range queries {
		key := readWitnessKey(q)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows, e := s.Query(q.SQL, q.Args...)
		if e != nil {
			return nil, e
		}
		result, e := witnessResult(rows)
		if e != nil {
			return nil, e
		}
		w.queries = append(w.queries, q)
		w.results = append(w.results, result)
	}
	return w, nil
}

// Verify runs only within Authority.Publish's already validated snapshot. It
// performs no writes, invokes no application callbacks, and never owns transport.
func (w *ReadWitness) Verify(ctx context.Context, c *sql.Conn) error {
	if w == nil || c == nil {
		return errors.New("recovery_read_witness_required")
	}
	for i, q := range w.queries {
		rows, e := c.QueryContext(ctx, q.SQL, q.Args...)
		if e != nil {
			return e
		}
		result, e := witnessResult(rows)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(result, w.results[i]) {
			return ErrReadWitnessChanged
		}
	}
	return nil
}

// Length-prefixed type-aware keys deduplicate repeated fixed reads in linear
// time, without conflating integer/real arguments or copying whole table state.
func readWitnessKey(q readWitnessQuery) string {
	b := binary.AppendUvarint(nil, uint64(len(q.SQL)))
	b = append(b, q.SQL...)
	b = binary.AppendUvarint(b, uint64(len(q.Args)))
	for _, a := range q.Args {
		switch v := a.(type) {
		case nil:
			b = append(b, 'n')
		case string:
			b = append(b, 's')
			b = binary.AppendUvarint(b, uint64(len(v)))
			b = append(b, v...)
		case []byte:
			b = append(b, 'b')
			b = binary.AppendUvarint(b, uint64(len(v)))
			b = append(b, v...)
		case int:
			b = append(b, 'i')
			b = binary.AppendVarint(b, int64(v))
		case int64:
			b = append(b, 'j')
			b = binary.AppendVarint(b, v)
		case float64:
			b = append(b, 'f')
			b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
		case bool:
			if v {
				b = append(b, 't')
			} else {
				b = append(b, 'F')
			}
		}
	}
	return string(b)
}
