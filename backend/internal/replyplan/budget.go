package replyplan

const EvidenceScopeContract = "Delivery coverage describes only the returned tool result. Retrieval scope never establishes exhaustive history: historyCoverage not_established forbids inferring only, all, or none historically from result counts, even if the plan suggests it. Preserve narrower state qualifications. Coverage metadata does not verify the truth of declarations."

func RetrievalCoverage(tool string) *Object {
	scope := "tool_defined_result"
	switch tool {
	case "memory.search":
		scope = "current_authorized_query_results"
	case "memory.person":
		scope = "current_authorized_person_results"
	case "messages.get":
		scope = "requested_message_ids"
	case "messages.around", "messages.byUser":
		scope = "bounded_message_window"
	case "memory.history", "memory.commitmentHistory", "memory.commitmentTimeline":
		scope = "retained_history_window"
	}
	return obj(member("scope", scope), member("historyCoverage", "not_established"))
}

type EvidenceBudget struct{ maxBytes float64 }

func NewEvidenceBudget(maxBytes *float64) (*EvidenceBudget, error) {
	n := float64(131072)
	if maxBytes != nil {
		n = *maxBytes
	}
	if !safeInteger(n) || n < 1 {
		return nil, errCode("planner_invalid_evidence_budget")
	}
	return &EvidenceBudget{maxBytes: n}, nil
}
func (b *EvidenceBudget) Measure(messages []*Object) (int, error) {
	expanded, err := ExpandContextMessages(messages)
	if err != nil {
		return 0, err
	}
	s, err := stringify(objectsValue(expanded))
	return len(s), err
}
func (b *EvidenceBudget) AssertWithin(messages []*Object) error {
	n, err := b.Measure(messages)
	if err != nil {
		return err
	}
	if float64(n) > b.maxBytes {
		return errCode("planner_evidence_budget_exhausted")
	}
	return nil
}

type DeliveredReceipt struct {
	Receipt *Object
	Message *Object
}

func (b *EvidenceBudget) Receipt(messages []*Object, receipt *Object, toolCallID string, serialize func(any) (string, error)) (DeliveredReceipt, error) {
	if serialize == nil {
		serialize = stringify
	}
	receipt = receipt.Clone()
	set(receipt, "retrievalCoverage", RetrievalCoverage(str(get(receipt, "toolName"))))
	envelope := func(value *Object) (*Object, error) {
		content, err := serialize(value)
		if err != nil {
			return nil, err
		}
		return obj(member("role", "tool"), member("toolCallId", toolCallID), member("content", content)), nil
	}
	fits := func(message *Object) (bool, error) {
		n, err := b.Measure(append(append([]*Object{}, messages...), message))
		return float64(n) <= b.maxBytes, err
	}
	tools := append([]string{"memory.search", "memory.person", "memory.relations", "memory.commitments", "memory.commitmentHistory", "memory.history", "experience.outcomes"}, workspaceStateTools...)
	records, ok := get(receipt, "value").([]any)
	if !includes(tools, str(get(receipt, "toolName"))) || !ok {
		s, err := stringify(get(receipt, "value"))
		if err != nil {
			return DeliveredReceipt{}, err
		}
		if len(s) > 65536 {
			return DeliveredReceipt{}, errCode("planner_evidence_too_large")
		}
		message, err := envelope(receipt)
		if err != nil {
			return DeliveredReceipt{}, err
		}
		if err := b.AssertWithin(append(append([]*Object{}, messages...), message)); err != nil {
			return DeliveredReceipt{}, err
		}
		return DeliveredReceipt{receipt, message}, nil
	}
	upstream := get(receipt, "coverage")
	candidate := func(count int) *Object {
		out := receipt.Clone()
		values := make([]any, count)
		copy(values, records[:count])
		set(out, "value", values)
		status := "limited"
		if count == len(records) && get(upstream, "status") != "limited" {
			status = "complete"
		}
		omitted := float64(len(records) - count)
		if n, ok := get(upstream, "omittedRecords").(float64); ok {
			omitted += n
		}
		coverage := obj(member("scope", "returned_tool_result"), member("status", status), member("returnedRecords", float64(count)), member("omittedRecords", omitted))
		if count < len(records) || get(upstream, "reason") == "planner_evidence_budget" {
			set(coverage, "reason", "planner_evidence_budget")
		}
		set(out, "coverage", coverage)
		return out
	}
	full := candidate(len(records))
	message, err := envelope(full)
	if err != nil {
		return DeliveredReceipt{}, err
	}
	fullFits, err := fits(message)
	if err != nil {
		return DeliveredReceipt{}, err
	}
	if fullFits {
		return DeliveredReceipt{full, message}, nil
	}
	omitted := candidate(0)
	message, err = envelope(omitted)
	if err != nil {
		return DeliveredReceipt{}, err
	}
	if err := b.AssertWithin(append(append([]*Object{}, messages...), message)); err != nil {
		return DeliveredReceipt{}, err
	}
	return DeliveredReceipt{omitted, message}, nil
}
