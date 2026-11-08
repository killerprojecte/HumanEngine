package learningbatch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func flowProjection(e FlowEvent) any {
	m := map[string]any{"stage": e.Stage, "status": e.Status}
	if e.Reason != "" {
		m["reason"] = e.Reason
	}
	if e.NextStep != "" {
		m["nextStep"] = e.NextStep
	}
	if e.Role != "" {
		m["role"] = e.Role
	}
	if len(e.Counts) > 0 {
		m["counts"] = e.Counts
	}
	return m
}
func normalizeFlow(v any) any {
	raw, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(raw, &x)
	return x
}
func TestPersistenceDiagnosticsPreservePhysicalEffectBoundary(t *testing.T) {
	for _, status := range []string{"saved", "rejected", "ignored", "unknown"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := []FlowEvent{}
			b := &batch{ctx: ctx, r: &Runner{d: Dependencies{Event: func(e FlowEvent) { events = append(events, e) }}}}
			result, e := b.observedPersistence(func() (proposalResult, error) {
				cancel()
				if status == "unknown" {
					return proposalResult{}, errors.New("synthetic_after_commit")
				}
				return proposalResult{status: status}, nil
			})
			if (e != nil) != (status == "unknown") || result.status != "" && result.status != status {
				t.Fatal(result, e)
			}
			statuses := []string{}
			for _, v := range events {
				statuses = append(statuses, v.Status)
			}
			want := "cancelled"
			if status == "saved" || status == "unknown" {
				want = "partial"
			}
			if !reflect.DeepEqual(statuses, []string{"started", "cancel_requested", want}) {
				t.Fatal(statuses)
			}
			terminal := events[len(events)-1]
			if status == "unknown" && terminal.Counts["unconfirmedCandidateCount"] != 1 {
				t.Fatal(terminal)
			}
			if status == "saved" && terminal.Counts["savedCount"] != 1 {
				t.Fatal(terminal)
			}
		})
	}
}
func TestDiagnosticSinkPanicCannotChangePersistence(t *testing.T) {
	b := &batch{ctx: context.Background(), r: &Runner{d: Dependencies{Event: func(FlowEvent) { panic("synthetic") }}}}
	r, e := b.observedPersistence(func() (proposalResult, error) { return proposalResult{status: "saved"}, nil })
	if e != nil || r.status != "saved" {
		t.Fatal(r, e)
	}
}
