package knowledge

// ParseProposal validates the complete strict model proposal and emits schema
// field order, just as KnowledgeProposalSchema.parse does. Semantic graph
// validation (witnesses, source spans, exposed endpoints) belongs to admission.
func ParseProposal(v any) (*Object, error) {
	point := func(subject bool) decoder {
		return func(v any) (any, error) {
			switch get(v, "kind") {
			case "self":
				if subject {
					return shape(v, f("kind", enumeration("self")), f("personId", bounded(1, 512)))
				}
			case "new":
				return shape(v, f("kind", enumeration("new")), f("entityKind", enumeration("plan", "event", "topic", "tool", "organization", "concept", "task")), f("title", bounded(1, 500)))
			case "existing":
				return shape(v, f("kind", enumeration("existing")), f("entityId", bounded(1, 512)))
			case "literal":
				if !subject {
					return endpoint(v)
				}
			}
			return nil, ErrSchema
		}
	}
	capped := func(min, max float64) decoder {
		return func(v any) (any, error) {
			n, e := safe(min)(v)
			if e != nil || num(n) > max {
				return nil, ErrSchema
			}
			return n, nil
		}
	}
	p, e := shape(v, f("registryVersion", one), f("predicate", enumeration("participates_in", "prefers_topic", "uses_tool", "member_of", "part_of", "depends_on", "has_property")), opt("property", enumeration("version", "count", "duration", "enabled")), f("subject", point(true)), f("object", point(false)), f("polarity", enumeration("positive", "negative")), f("modality", enumeration("asserted", "possible")), f("temporalScope", enumeration("current", "past", "future", "unspecified")), f("conditions", list(bounded(1, 2000), 0, 8)), opt("validFrom", instant), opt("validUntil", instant), f("sourceSpans", list(func(v any) (any, error) {
		return shape(v, f("messageId", bounded(1, 512)), f("partIndex", capped(0, 4095)), f("start", capped(0, 1000000)), f("end", capped(1, 1000000)), f("stance", enumeration("self", "asserted", "reported", "quoted")))
	}, 1, 8)))
	if e != nil {
		return nil, e
	}
	if (get(p, "predicate") == "has_property") != has(p, "property") {
		return nil, ErrSchema
	}
	if has(p, "validFrom") && has(p, "validUntil") {
		a, _, _ := utcDate.ParseMillis(str(get(p, "validFrom")))
		b, _, _ := utcDate.ParseMillis(str(get(p, "validUntil")))
		if a > b {
			return nil, ErrSchema
		}
	}
	return p, nil
}
