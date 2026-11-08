package sourceadmission

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
)

// PrepareCommitment implements the outer proposal's declaration rewrite only.
// The command itself is deliberately parsed later by commitment admission, after
// the backing checkpoint, matching the source's rejected-command side effects.
func PrepareCommitment(ctx, candidate *Object) (Preparation, error) {
	reject := func(reason string) (Preparation, error) { return Preparation{Reason: reason}, nil }
	if get(candidate, "epistemicKind") != "explicit" {
		return reject("commitment_explicit_declaration_required")
	}
	sources := arr(get(candidate, "sources"))
	var source, evidence *Object
	if len(sources) == 1 {
		source = object(sources[0])
		for _, item := range arr(get(ctx, "sourceEvidence")) {
			if get(item, "sceneId") == get(source, "sceneId") && get(item, "messageId") == get(source, "messageId") && get(item, "senderId") == get(source, "senderId") && truthy(get(item, "available")) && !truthy(get(item, "isBot")) {
				evidence = object(item)
				break
			}
		}
	}
	units, err := jsjson.UTF16(str(get(evidence, "sourceText")))
	if err != nil {
		return Preparation{}, err
	}
	literal := false
	for _, unit := range units {
		if !declarationWhitespace(unit) {
			literal = true
			break
		}
	}
	if source == nil || !literal || len(units) > 2000 || get(get(candidate, "subject"), "kind") != "self" || get(get(candidate, "subject"), "sourceMessageId") != get(source, "messageId") {
		return reject("commitment_literal_self_source_required")
	}
	if len(arr(get(candidate, "relations"))) > 0 || truthy(get(candidate, "supersedesId")) || truthy(get(candidate, "mergeIntoId")) {
		return reject("commitment_mixed_write_denied")
	}
	declaration, err := digest([]any{get(source, "sceneId"), get(source, "messageId"), get(source, "senderId"), get(evidence, "eventRef"), get(candidate, "commitment")})
	if err != nil {
		return Preparation{}, err
	}
	rewritten := clone(candidate).(*Object)
	_ = rewritten.Set("content", "Model-extracted plan declaration ("+declarationKind(get(candidate, "commitment"))+"); reference "+declaration+". Not independently verified.")
	_ = rewritten.Set("kind", "claim")
	_ = rewritten.Set("epistemicKind", "explicit")
	for _, key := range []string{"occurredAt", "validFrom", "validUntil"} {
		rewritten.Delete(key)
	}
	trusted := clone(ctx).(*Object)
	_ = trusted.Set("provenance", "source-only")
	_ = trusted.Set("dependencyMemoryIds", []any{})
	_ = trusted.Set("dependencyParentReferences", []any{})
	_ = trusted.Set("internalDeclaration", true)
	return Preparation{OK: true, Context: trusted, Candidate: rewritten}, nil
}

func declarationWhitespace(c uint16) bool {
	return c == 9 || c == 10 || c == 11 || c == 12 || c == 13 || c == 32 || c == 160 || c == 0xfeff || c == 0x1680 || c >= 0x2000 && c <= 0x200a || c == 0x2028 || c == 0x2029 || c == 0x202f || c == 0x205f || c == 0x3000
}

// Preserve template interpolation before the command schema rejects bad kinds.
func declarationKind(command any) string {
	if !has(command, "kind") {
		return "undefined"
	}
	return declarationString(get(command, "kind"))
}
func declarationString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		s, _ := jsjson.Stringify(x)
		return s
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			if v != nil {
				parts[i] = declarationString(v)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}
