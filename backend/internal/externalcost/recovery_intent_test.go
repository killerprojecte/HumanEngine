package externalcost

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestStorageReceiptRecoveryIntentRefinement(t *testing.T) {
	const good = `{"recordedAt":"2024-02-29T01:02:03Z","correctionId":null,"receipt":{"status":"known","amountNanoUsd":-0,"sourceId":"source","periodEnd":"2024-02-29","periodStart":"2024-02-01","billingDate":"2024-02-29","currency":"USD","category":"tool","id":"r"},"revision":1,"schemaVersion":1}`
	receipt, err := Parse(good)
	if err != nil {
		t.Fatal(err)
	}
	event, err := receipt.RecoveryEvent()
	if err != nil {
		t.Fatal(err)
	}
	if event.Receipt.Revision != 1 || event.Receipt.Receipt.AmountNanoUSD == nil || *event.Receipt.Receipt.AmountNanoUSD != 0 {
		t.Fatal("adapter changed receipt", event)
	}
	// Compile-time proof the domain adapter produces the actual sealed intent.
	var _ recovery.Revocation = event
	for _, raw := range []string{
		`{"schemaVersion":1,"revision":1,"receipt":{"id":"r","category":"tool","currency":"USD","billingDate":"2024-02-29","periodStart":"2024-02-01","periodEnd":"2024-02-29","sourceId":"source","status":"unknown"},"correctionId":"correction","recordedAt":"2024-02-29T01:02:03Z"}`,
		`{"schemaVersion":1,"revision":2,"receipt":{"id":"r","category":"tool","currency":"USD","billingDate":"2024-02-29","periodStart":"2024-02-01","periodEnd":"2024-02-29","sourceId":"source","status":"unknown"},"correctionId":null,"recordedAt":"2024-02-29T01:02:03Z"}`,
	} {
		r, err := Parse(raw)
		if err != nil {
			t.Fatal("storage contract should accept", err)
		}
		if _, err = r.RecoveryEvent(); err == nil {
			t.Fatal("recovery refinement lost")
		}
	}
	if _, err := (Receipt{}).RecoveryEvent(); err == nil {
		t.Fatal("zero receipt accepted")
	}
}
