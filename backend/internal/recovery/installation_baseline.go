package recovery

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var errBaselineExists = errors.New("installation_baseline_exists")

// EnsureInstallationBaseline runs on the configured online writer before Engine.
// Census is protected by main BEGIN IMMEDIATE; durable intent survives failed ACK.
func (a *Authority) EnsureInstallationBaseline(ctx context.Context, main *sql.DB, census func(*MainRead) ([]EligibilityChange, error)) error {
	_, err := a.PrepareWrite(ctx, main, func(r *MainRead) (WritePlan, error) {
		rows, e := r.Query("SELECT value FROM main.recovery_gate WHERE id=1")
		if e != nil {
			return WritePlan{}, e
		}
		var raw string
		if !rows.Next() {
			rows.Close()
			return WritePlan{}, errors.New("recovery_gate_invalid")
		}
		e = rows.Scan(&raw)
		e = errors.Join(e, rows.Close())
		if e != nil {
			return WritePlan{}, e
		}
		gate, _, e := decodeGate(raw)
		if e != nil {
			return WritePlan{}, e
		}
		exists, e := a.installationHasBaseline(ctx, gate.Head)
		if e != nil {
			return WritePlan{}, e
		}
		if exists {
			return WritePlan{}, errBaselineExists
		}
		changes, e := census(r)
		if e != nil {
			return WritePlan{}, e
		}
		operation, e := installationUUID()
		if e != nil {
			return WritePlan{}, e
		}
		return WritePlan{Intents: []Revocation{AssertionEligibility{OperationID: operation, Mode: "baseline", Changes: changes}}, MinimumReader: 11, Apply: func(*MainMutation) error { return nil }}, nil
	})
	if onlyBaselineExists(err) {
		return nil
	}
	return err
}
func (a *Authority) installationHasBaseline(ctx context.Context, expected Head) (found bool, err error) {
	if err = a.mu.LockContext(ctx); err != nil {
		return false, err
	}
	defer a.mu.Unlock()
	if err = beginOwned(ctx, a.conn, "BEGIN"); err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, rollbackOwned(a.conn)) }()
	head, err := a.scan(ctx)
	if err != nil {
		return false, err
	}
	if head != expected {
		return false, errors.New("recovery_authority_head_changed")
	}
	rows, err := a.conn.QueryContext(ctx, "SELECT payload FROM main.revocations ORDER BY sequence")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return false, e
		}
		o, ok := v.(*jsjson.Object)
		if !ok {
			return false, errors.New("recovery_unknown_revocation")
		}
		kind, _ := o.Get("kind")
		mode, _ := o.Get("mode")
		if kind == "assertion_eligibility" && mode == "baseline" {
			found = true
		}
	}
	return found, rows.Err()
}

// Do not suppress cleanup/cancellation joined to the no-op sentinel.
func onlyBaselineExists(err error) bool {
	if err == errBaselineExists {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, e := range children {
			if !onlyBaselineExists(e) {
				return false
			}
		}
		return true
	}
	return false
}
