package recovery

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// ExternalCostInput is the typed accounting metadata admitted by the source
// contract. Amount is present only for known receipts, including a known zero.
type ExternalCostInput struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`
	Currency      string   `json:"currency"`
	BillingDate   string   `json:"billingDate"`
	PeriodStart   string   `json:"periodStart"`
	PeriodEnd     string   `json:"periodEnd"`
	SourceID      string   `json:"sourceId"`
	Status        string   `json:"status"`
	AmountNanoUSD *float64 `json:"amountNanoUsd,omitempty"`
}
type ExternalCostReceiptData struct {
	SchemaVersion int               `json:"schemaVersion"`
	Revision      float64           `json:"revision"`
	Receipt       ExternalCostInput `json:"receipt"`
	CorrectionID  *string           `json:"correctionId"`
	RecordedAt    string            `json:"recordedAt"`
}
type ExternalCostReceipt struct {
	Receipt ExternalCostReceiptData `json:"receipt"`
}

func (e ExternalCostReceipt) recoveryRecord() (string, any) {
	return "external_cost_receipt", struct {
		Kind string `json:"kind"`
		ExternalCostReceipt
	}{"external_cost_receipt", e}
}

// ParseExternalCostReceiptIntent bridges a storage receipt to a typed intent
// without importing externalcost (which depends on recovery). This is a strict,
// single-domain constructor, not a raw authority-event bypass. Unknown fields,
// invalid revisions, and invalid correction relationships are rejected before
// conversion. Existing authority history never passes through this constructor.
func ParseExternalCostReceiptIntent(raw string) (ExternalCostReceipt, error) {
	value, err := jsjson.Parse(raw)
	if err != nil {
		return ExternalCostReceipt{}, err
	}
	object, ok := value.(*jsjson.Object)
	if !ok {
		return ExternalCostReceipt{}, errors.New("recovery_external_cost_event_invalid")
	}
	event, _ := jsjson.NewObject(jsjson.Member{Key: "kind", Value: "external_cost_receipt"}, jsjson.Member{Key: "receipt", Value: object})
	encoded, err := jsjson.Stringify(event)
	if err != nil {
		return ExternalCostReceipt{}, err
	}
	if err = ValidateEvent(encoded); err != nil {
		return ExternalCostReceipt{}, err
	}
	// Validation proves all of these concrete types and required members.
	get := func(o *jsjson.Object, key string) any { v, _ := o.Get(key); return v }
	input := get(object, "receipt").(*jsjson.Object)
	text := func(o *jsjson.Object, key string) string { return get(o, key).(string) }
	data := ExternalCostReceiptData{SchemaVersion: 1, Revision: get(object, "revision").(float64), RecordedAt: text(object, "recordedAt"), Receipt: ExternalCostInput{
		ID: text(input, "id"), Category: text(input, "category"), Currency: text(input, "currency"), BillingDate: text(input, "billingDate"), PeriodStart: text(input, "periodStart"), PeriodEnd: text(input, "periodEnd"), SourceID: text(input, "sourceId"), Status: text(input, "status"),
	}}
	if v := get(object, "correctionId"); v != nil {
		s := v.(string)
		data.CorrectionID = &s
	}
	if v, has := input.Get("amountNanoUsd"); has {
		n := v.(float64)
		data.Receipt.AmountNanoUSD = &n
	}
	return ExternalCostReceipt{data}, nil
}
