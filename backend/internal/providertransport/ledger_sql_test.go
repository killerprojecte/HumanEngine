package providertransport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var ledgerTestContext = context.Background()

func ledgerMust(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func ledgerDatabase(t *testing.T) (*sql.DB, *SQLLedger) {
	t.Helper()
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "budget.sqlite")+"?_busy_timeout=5000")
	ledgerMust(t, e)
	t.Cleanup(func() { db.Close() })
	l, e := NewSQLLedger(ledgerTestContext, db, "boot", func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) })
	ledgerMust(t, e)
	return db, l
}

type ledgerFixture struct {
	Native struct {
		Descriptor               Descriptor
		Body, Usage, LimitedBody map[string]any
		Rows                     []map[string]any
		Coverage                 json.RawMessage
	}
	Sources map[string]string
	Base    Reservation
	Policy  BudgetPolicy
	Full    map[string]any
	Cases   []struct {
		Name  string
		Steps []struct {
			Method string
			Args   []json.RawMessage
			Error  string
			Result json.RawMessage
		}
		Rows            []map[string]any
		Reconciliations []map[string]any
	}
	Quotes []struct {
		Request Reservation
		Amount  int64
		Error   string
	}
	ExactSubtotal int64
}

func ledgerFixtureLoad(t *testing.T) ledgerFixture {
	t.Helper()
	raw, e := os.ReadFile("testdata/budget-ledger.json")
	ledgerMust(t, e)
	var f ledgerFixture
	ledgerMust(t, json.Unmarshal(raw, &f))
	for p, hash := range f.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", p))
		ledgerMust(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatalf("budget source oracle stale: %s", p)
		}
	}
	return f
}
func ledgerDecode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	ledgerMust(t, json.Unmarshal(raw, &value))
	return value
}
func coverageMap(c BudgetCoverage) map[string]any {
	return map[string]any{"currency": c.Currency, "windowStart": c.WindowStart, "externalKnownNanoUsd": c.ExternalKnownNanoUSD, "externalUnknownReceipts": c.ExternalUnknownReceipts, "knownNanoUsd": c.KnownNanoUSD, "heldNanoUsd": c.HeldNanoUSD, "carriedNanoUsd": c.CarriedNanoUSD, "unresolvedNanoUsd": c.UnresolvedNanoUSD, "admissionExposureNanoUsd": c.AdmissionExposureNanoUSD, "remainingNanoUsd": c.RemainingNanoUSD, "laneExposureNanoUsd": c.LaneExposureNanoUSD, "overrunAttempts": c.OverrunAttempts, "unboundedAttempts": c.UnboundedAttempts}
}
func ledgerEqualJSON(t *testing.T, got any, want json.RawMessage) {
	t.Helper()
	b, e := json.Marshal(got)
	ledgerMust(t, e)
	var a, z any
	ledgerMust(t, json.Unmarshal(b, &a))
	ledgerMust(t, json.Unmarshal(want, &z))
	if !reflect.DeepEqual(a, z) {
		t.Fatalf("got %s\nsource %s", b, want)
	}
}
func TestSQLLedgerActualSourceScenarios(t *testing.T) {
	f := ledgerFixtureLoad(t)
	for _, test := range f.Cases {
		t.Run(test.Name, func(t *testing.T) {
			db, l := ledgerDatabase(t)
			for index, step := range test.Steps {
				t.Run(fmt.Sprintf("%02d_%s", index, step.Method), func(t *testing.T) {
					var e error
					id := func() string { return ledgerDecode[string](t, step.Args[0]) }
					switch step.Method {
					case "reserve":
						e = l.Reserve(ledgerTestContext, ledgerDecode[Reservation](t, step.Args[0]), ledgerDecode[BudgetPolicy](t, step.Args[1]))
					case "start":
						var got bool
						got, e = l.Start(ledgerTestContext, id())
						if e == nil {
							ledgerEqualJSON(t, got, step.Result)
						}
					case "settle":
						e = l.Settle(ledgerTestContext, id(), ledgerDecode[map[string]any](t, step.Args[1]))
					case "unknown":
						e = l.Unknown(ledgerTestContext, id())
					case "release":
						e = l.Release(ledgerTestContext, id())
					case "reconcile":
						e = l.Reconcile(ledgerTestContext, id(), ledgerDecode[map[string]any](t, step.Args[1]), ledgerDecode[string](t, step.Args[2]))
					case "recoverDeadBoot":
						if id() == "dead" {
							_, e = db.Exec("UPDATE budget_attempts SET boot_id='dead'")
							ledgerMust(t, e)
						}
						var n int64
						n, e = l.RecoverDeadBoot(ledgerTestContext, id())
						if e == nil {
							ledgerEqualJSON(t, n, step.Result)
						}
					case "coverage":
						if test.Name == "old_day_holds" {
							_, e = db.Exec("UPDATE budget_attempts SET window_start='2026-11-07'")
							ledgerMust(t, e)
						}
						var c BudgetCoverage
						c, e = l.Coverage(ledgerTestContext, ledgerDecode[BudgetPolicy](t, step.Args[0]))
						if e == nil {
							ledgerEqualJSON(t, coverageMap(c), step.Result)
						}
					default:
						t.Fatal(step.Method)
					}
					if step.Error != "" {
						if e == nil || e.Error() != step.Error {
							t.Fatalf("got %v source %s", e, step.Error)
						}
					} else {
						ledgerMust(t, e)
					}
				})
			}
			for _, want := range test.Rows {
				var payload, hash, policy, state, boot, day, created string
				var reserved, known, held int64
				var started, settled, usage, receipt sql.NullString
				var overrun, unbounded int64
				ledgerMust(t, db.QueryRow("SELECT payload,payload_hash,policy,state,boot_id,window_start,created_at,reserved_nano,known_nano,held_nano,started_at,settled_at,usage,receipt_hash,overrun,unbounded_exposure FROM budget_attempts WHERE id=?", want["id"]).Scan(&payload, &hash, &policy, &state, &boot, &day, &created, &reserved, &known, &held, &started, &settled, &usage, &receipt, &overrun, &unbounded))
				nullable := func(s sql.NullString) any {
					if s.Valid {
						return s.String
					}
					return nil
				}
				got := map[string]any{"payload": payload, "payload_hash": hash, "policy": policy, "state": state, "boot_id": boot, "window_start": day, "created_at": created, "reserved_nano": reserved, "known_nano": known, "held_nano": held, "started_at": nullable(started), "settled_at": nullable(settled), "usage": nullable(usage), "receipt_hash": nullable(receipt), "overrun": overrun, "unbounded_exposure": unbounded}
				for k, v := range got {
					b, _ := json.Marshal(v)
					w, _ := json.Marshal(want[k])
					if string(b) != string(w) {
						t.Fatalf("%s: got %s source %s", k, b, w)
					}
				}
			}
			var count int
			ledgerMust(t, db.QueryRow("SELECT count(*) FROM budget_attempts").Scan(&count))
			if count != len(test.Rows) {
				t.Fatal("row count mismatch", count)
			}
			ledgerMust(t, db.QueryRow("SELECT count(*) FROM budget_reconciliations").Scan(&count))
			if count != len(test.Reconciliations) {
				t.Fatal("evidence count mismatch", count)
			}
			for _, want := range test.Reconciliations {
				var id, hash, at string
				ledgerMust(t, db.QueryRow("SELECT attempt_id,receipt_hash,reconciled_at FROM budget_reconciliations WHERE evidence_id=?", want["evidence_id"]).Scan(&id, &hash, &at))
				if id != want["attempt_id"] || hash != want["receipt_hash"] || at != want["reconciled_at"] {
					t.Fatal("reconciliation mismatch")
				}
			}
		})
	}
}
func TestSQLLedgerExactDecimalSource(t *testing.T) {
	f := ledgerFixtureLoad(t)
	for _, q := range f.Quotes {
		got, e := QuoteEstimate(q.Request.Estimate, q.Request.Prices, "")
		if q.Error != "" {
			if e == nil || e.Error() != q.Error {
				t.Fatalf("%v source %s", e, q.Error)
			}
		} else {
			ledgerMust(t, e)
			if got != q.Amount {
				t.Fatalf("%d source %d", got, q.Amount)
			}
		}
	}
	got, e := exactBudgetSubtotal([]budgetBucket{{1, 1e-7}, {1, 1e-7}})
	ledgerMust(t, e)
	if got != f.ExactSubtotal {
		t.Fatal(got, f.ExactSubtotal)
	}
}
func ledgerReceipt(t *testing.T, id, status, date string, amount int64) externalcost.Receipt {
	t.Helper()
	a := ""
	if status == "known" {
		a = fmt.Sprintf(`,"amountNanoUsd":%d`, amount)
	}
	r, e := externalcost.Parse(fmt.Sprintf(`{"schemaVersion":1,"revision":1,"receipt":{"id":%q,"category":"tool","currency":"USD","billingDate":%q,"periodStart":%q,"periodEnd":%q,"sourceId":"synthetic","status":%q%s},"correctionId":null,"recordedAt":"2026-11-08T12:00:00.000Z"}`, id, date, date, date, status, a))
	ledgerMust(t, e)
	return r
}
func TestSQLLedgerExternalCoverageStartBoundaryAndRollback(t *testing.T) {
	f := ledgerFixtureLoad(t)
	db, l := ledgerDatabase(t)
	ledgerMust(t, l.Reserve(ledgerTestContext, f.Base, f.Policy))
	s := externalcost.Storage{DB: db}
	ledgerMust(t, s.Put(ledgerTestContext, ledgerReceipt(t, "known", "known", "2026-11-08", 100)))
	ledgerMust(t, s.Put(ledgerTestContext, ledgerReceipt(t, "unknown", "unknown", "2026-11-07", 0)))
	c, e := l.Coverage(ledgerTestContext, f.Policy)
	ledgerMust(t, e)
	if c.ExternalKnownNanoUSD != 100 || c.ExternalUnknownReceipts != 1 || c.RemainingNanoUSD != 0 {
		t.Fatal(c)
	}
	if _, e = l.Start(ledgerTestContext, f.Base.ID); e == nil || e.Error() != "budget_external_cost_unknown" {
		t.Fatal(e)
	}
	v, e := l.Get(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	if v.State != "reserved" {
		t.Fatal(v)
	}
	_, e = db.Exec("DELETE FROM " + externalcost.Table + " WHERE id='unknown'")
	ledgerMust(t, e)
	_, e = l.Start(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	_, e = db.Exec("CREATE TRIGGER reject_settle BEFORE UPDATE ON budget_attempts WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'synthetic_failure'); END")
	ledgerMust(t, e)
	e = l.Settle(ledgerTestContext, f.Base.ID, f.Full)
	if e == nil || !strings.Contains(e.Error(), "synthetic_failure") {
		t.Fatal(e)
	}
	v, e = l.Get(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	if v.State != "started" || v.ReceiptHash.Valid {
		t.Fatal("failed settlement leaked", v)
	}
}
func TestSQLLedgerRestartOwnershipAndConcurrentAdmission(t *testing.T) {
	f := ledgerFixtureLoad(t)
	db, l := ledgerDatabase(t)
	ledgerMust(t, l.Reserve(ledgerTestContext, f.Base, f.Policy))
	other, e := NewSQLLedger(ledgerTestContext, db, "new-boot", l.Now)
	ledgerMust(t, e)
	if _, e = other.Start(ledgerTestContext, f.Base.ID); e == nil || e.Error() != "budget_attempt_owner" {
		t.Fatal(e)
	}
	v, e := other.Get(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	if v.State != "reserved" {
		t.Fatal("constructor recovered another boot")
	}
	n, e := other.RecoverDeadBoot(ledgerTestContext, "boot")
	ledgerMust(t, e)
	if n != 1 {
		t.Fatal(n)
	}
	quote, e := QuoteEstimate(f.Base.Estimate, f.Base.Prices, "")
	ledgerMust(t, e)
	p := f.Policy
	p.DailyNanoUSD = quote
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			r := f.Base
			r.ID = fmt.Sprintf("new-%d", i)
			r.RequestID = r.ID
			results <- other.Reserve(ledgerTestContext, r, p)
		}(i)
	}
	success, denied := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if e.Error() == "budget_daily_limit" {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal(success, denied)
	}
}

func TestSQLLedgerNativeGateActualSourceLifecycle(t *testing.T) {
	f := ledgerFixtureLoad(t)
	db, l := ledgerDatabase(t)
	g := NativeGate{Ledger: l, Policy: func(context.Context) (BudgetPolicy, error) { return f.Policy, nil }, Mode: "best_effort", MaxOutputTokens: 10}
	ctx, e := WithBudgetRequest(WithBudgetContext(ledgerTestContext, "native-turn", "maintenance"))
	ledgerMust(t, e)
	a, e := g.Prepare(ctx, f.Native.Descriptor, f.Native.Body)
	ledgerMust(t, e)
	want, _ := json.Marshal(f.Native.LimitedBody)
	ledgerEqualJSON(t, a.Body(), want)
	ledgerMust(t, a.Start(ctx))
	ledgerMust(t, a.Settle(ctx, f.Native.Usage))
	ledgerMust(t, a.Fail(ctx))
	a, e = g.Prepare(ctx, f.Native.Descriptor, f.Native.Body)
	ledgerMust(t, e)
	ledgerMust(t, a.Start(ctx))
	ledgerMust(t, a.Fail(ctx))
	ledgerMust(t, a.Fail(ctx))
	a, e = g.Prepare(ctx, f.Native.Descriptor, f.Native.Body)
	ledgerMust(t, e)
	ledgerMust(t, a.Fail(ctx))
	rows, e := db.Query("SELECT payload,state,reserved_nano,known_nano,held_nano,overrun,unbounded_exposure FROM budget_attempts ORDER BY json_extract(payload,'$.attempt')")
	ledgerMust(t, e)
	var got []map[string]any
	for rows.Next() {
		var raw, state string
		var reserved, known, held, overrun, unbounded int64
		ledgerMust(t, rows.Scan(&raw, &state, &reserved, &known, &held, &overrun, &unbounded))
		var request map[string]any
		ledgerMust(t, json.Unmarshal([]byte(raw), &request))
		got = append(got, map[string]any{"attempt": request["attempt"], "lane": request["lane"], "turnId": request["turnId"], "estimate": request["estimate"], "state": state, "reservedNanoUsd": reserved, "knownNanoUsd": known, "heldNanoUsd": held, "overrun": overrun, "unboundedExposure": unbounded})
	}
	ledgerMust(t, rows.Err())
	ledgerMust(t, rows.Close())
	want, _ = json.Marshal(f.Native.Rows)
	ledgerEqualJSON(t, got, want)
	coverage, e := l.Coverage(ctx, f.Policy)
	ledgerMust(t, e)
	ledgerEqualJSON(t, coverageMap(coverage), f.Native.Coverage)
}

func TestSQLLedgerDiskReopenAndEvidenceAtomicity(t *testing.T) {
	f := ledgerFixtureLoad(t)
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	db, e := sql.Open("sqlite3", path)
	ledgerMust(t, e)
	l, e := NewSQLLedger(ledgerTestContext, db, "boot", func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) })
	ledgerMust(t, e)
	ledgerMust(t, l.Reserve(ledgerTestContext, f.Base, f.Policy))
	_, e = l.Start(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	ledgerMust(t, l.Unknown(ledgerTestContext, f.Base.ID))
	ledgerMust(t, db.Close())
	db, e = sql.Open("sqlite3", path)
	ledgerMust(t, e)
	defer db.Close()
	l, e = NewSQLLedger(ledgerTestContext, db, "second", l.Now)
	ledgerMust(t, e)
	v, e := l.Get(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	if v.State != "unknown" || v.HeldNanoUSD == 0 {
		t.Fatal(v)
	}
	_, e = db.Exec("CREATE TRIGGER reject_evidence BEFORE INSERT ON budget_reconciliations BEGIN SELECT RAISE(ABORT,'synthetic_evidence_failure'); END")
	ledgerMust(t, e)
	if e = l.Reconcile(ledgerTestContext, f.Base.ID, f.Full, "evidence"); e == nil {
		t.Fatal("expected evidence rollback")
	}
	v, e = l.Get(ledgerTestContext, f.Base.ID)
	ledgerMust(t, e)
	if v.State != "unknown" || v.ReceiptHash.Valid || v.KnownNanoUSD != 0 {
		t.Fatal("reconciliation leaked", v)
	}
	_, e = db.Exec("DROP TRIGGER reject_evidence")
	ledgerMust(t, e)
	ledgerMust(t, l.Reconcile(ledgerTestContext, f.Base.ID, f.Full, "evidence"))
}
