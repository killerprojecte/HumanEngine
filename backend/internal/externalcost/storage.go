// Package externalcost implements administrator-declared accounting metadata.
// Storage does not enroll a database, acknowledge recovery, or grant readiness.
package externalcost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const Table = "budget_external_cost_revisions"

var columns = []string{"id", "revision", "payload", "payload_hash", "correction_id", "recorded_at"}

type SQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type Storage struct{ DB SQL }
type Receipt struct{ value *jsjson.Object }

func (r Receipt) ID() string {
	v, _ := r.value.Get("receipt")
	id, _ := v.(*jsjson.Object).Get("id")
	return id.(string)
}
func (r Receipt) Revision() int { v, _ := r.value.Get("revision"); return int(v.(float64)) }
func (r Receipt) JSON() string {
	if r.value == nil {
		return "null"
	}
	s, _ := jsjson.Stringify(r.value)
	return s
}
func (r Receipt) Hash() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(r.JSON()))) }
func ordered(o *jsjson.Object, keys []string) *jsjson.Object {
	n := new(jsjson.Object)
	for _, k := range keys {
		if v, ok := o.Get(k); ok {
			_ = n.Set(k, v)
		}
	}
	return n
}

// Parse uses the storage schema, not the stricter recovery refinement. Zod
// constructs fields in schema order; hashing raw JSON or sorted maps is wrong.
func Parse(raw string) (Receipt, error) {
	v, err := jsjson.Parse(raw)
	o, ok := v.(*jsjson.Object)
	if err != nil || !ok {
		return Receipt{}, errors.New("external_cost_storage_invalid")
	}
	return parseObject(o)
}
func parseObject(o *jsjson.Object) (Receipt, error) {
	// Reuse the complete recovery structural validation, changing ONLY its extra
	// revision/correction relationship on a clone, after checking the original ID.
	c, has := o.Get("correctionId")
	if !has {
		return Receipt{}, errors.New("external_cost_storage_invalid")
	}
	probe := o.Clone()
	rev, _ := o.Get("revision")
	if c != nil {
		test := o.Clone()
		_ = test.Set("revision", float64(2))
		if !validProbe(test) {
			return Receipt{}, errors.New("external_cost_storage_invalid")
		}
	}
	if rev == float64(1) {
		_ = probe.Set("correctionId", nil)
	} else {
		_ = probe.Set("correctionId", "validation")
	}
	if !validProbe(probe) {
		return Receipt{}, errors.New("external_cost_storage_invalid")
	}
	n := ordered(o, []string{"schemaVersion", "revision", "receipt", "correctionId", "recordedAt"})
	input, _ := o.Get("receipt")
	_ = n.Set("receipt", ordered(input.(*jsjson.Object), []string{"id", "category", "currency", "billingDate", "periodStart", "periodEnd", "sourceId", "status", "amountNanoUsd"}))
	return Receipt{n}, nil
}
func validProbe(o *jsjson.Object) bool {
	e, _ := jsjson.NewObject(jsjson.Member{Key: "kind", Value: "external_cost_receipt"}, jsjson.Member{Key: "receipt", Value: o})
	s, err := jsjson.Stringify(e)
	return err == nil && recovery.ValidateEvent(s) == nil
}
func equal(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && x == y && (x != 0 || math.Signbit(x) == math.Signbit(y))
	case *jsjson.Object:
		y, ok := b.(*jsjson.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, ok := y.Get(m.Key)
			if !ok || !equal(m.Value, v) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
func (s Storage) AssertSupported(ctx context.Context) (bool, error) {
	rows, err := s.query(ctx, "SELECT name,type FROM sqlite_master WHERE name GLOB 'budget_external_cost_*' AND type IN ('table','view')")
	if err != nil {
		return false, err
	}
	count := 0
	valid := true
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return false, err
		}
		count++
		valid = valid && name == Table && kind == "table"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if count != 1 || !valid {
		return false, errors.New("external_cost_schema_unsupported")
	}
	rows, err = s.query(ctx, "PRAGMA table_xinfo("+Table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk, &hidden); err != nil {
			return false, err
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	if !reflect.DeepEqual(names, columns) {
		return false, errors.New("external_cost_schema_unsupported")
	}
	return true, nil
}
func (s Storage) Ensure(ctx context.Context) error {
	ok, err := s.AssertSupported(ctx)
	if err != nil || ok {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `CREATE TABLE `+Table+`(id TEXT NOT NULL,revision INTEGER NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,correction_id TEXT UNIQUE,recorded_at TEXT NOT NULL,PRIMARY KEY(id,revision))`)
	return err
}

type scanner interface{ Scan(...any) error }

func decode(row scanner) (Receipt, error) {
	var id, rev, payload, hash, correction, recorded any
	if err := row.Scan(&id, &rev, &payload, &hash, &correction, &recorded); err != nil {
		return Receipt{}, err
	}
	raw, ok := payload.(string)
	if !ok {
		return Receipt{}, errors.New("external_cost_storage_invalid")
	}
	r, err := Parse(raw)
	if err != nil {
		return Receipt{}, err
	}
	c, _ := r.value.Get("correctionId")
	t, _ := r.value.Get("recordedAt")
	number := false
	switch n := rev.(type) {
	case int64:
		number = n == int64(r.Revision())
	case float64:
		number = n == float64(r.Revision())
	}
	if id != r.ID() || !number || hash != r.Hash() || correction != c || recorded != t {
		return Receipt{}, errors.New("external_cost_storage_invalid")
	}
	return r, nil
}
func (s Storage) find(ctx context.Context, where string, arg any) (*Receipt, error) {
	ok, err := s.AssertSupported(ctx)
	if err != nil || !ok {
		return nil, err
	}
	r, err := decode(s.row(ctx, "SELECT * FROM "+Table+" WHERE "+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
func (s Storage) Latest(ctx context.Context, id string) (*Receipt, error) {
	return s.find(ctx, "id=? ORDER BY revision DESC LIMIT 1", id)
}
func (s Storage) Correction(ctx context.Context, id string) (*Receipt, error) {
	return s.find(ctx, "correction_id=?", id)
}
func (s Storage) Current(ctx context.Context) ([]Receipt, error) {
	ok, err := s.AssertSupported(ctx)
	if err != nil || !ok {
		return []Receipt{}, err
	}
	rows, err := s.query(ctx, `SELECT r.* FROM `+Table+` r JOIN (SELECT id,max(revision) revision FROM `+Table+` GROUP BY id) h ON r.id=h.id AND r.revision=h.revision LIMIT 10001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var material []storedRow
	for rows.Next() {
		var row storedRow
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3], &row[4], &row[5]); err != nil {
			return nil, err
		}
		material = append(material, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(material) > 10000 {
		return nil, errors.New("external_cost_capacity")
	}
	out := []Receipt{}
	for _, row := range material {
		r, err := decode(row)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Put is storage-only and deliberately does not require consecutive revisions.
// The caller owns transaction/authority admission; production mutation is not wired.
func (s Storage) Put(ctx context.Context, input Receipt) error {
	r, err := Parse(input.JSON())
	if err != nil {
		return err
	} // Retain negative zero for strict equality with existing material.
	r.value = input.value
	if err = s.Ensure(ctx); err != nil {
		return err
	}
	old, err := decode(s.row(ctx, "SELECT * FROM "+Table+" WHERE id=? AND revision=?", r.ID(), r.Revision()))
	if err == nil {
		if !equal(old.value, r.value) {
			return errors.New("external_cost_conflict")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	c, _ := r.value.Get("correctionId")
	t, _ := r.value.Get("recordedAt")
	_, err = s.DB.ExecContext(ctx, "INSERT INTO "+Table+" VALUES(?,?,?,?,?,?)", r.ID(), r.Revision(), r.JSON(), r.Hash(), c, t)
	return err
}

// storedRow retains SQLite dynamic types while checking capacity before material.
type storedRow [6]any

func (r storedRow) Scan(dest ...any) error {
	for i := range r {
		*(dest[i].(*any)) = r[i]
	}
	return nil
}
