package assertions

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

func CanonicalProofKey(proof *Object) (string, error) {
	sources := []any{}
	for _, v := range array(get(proof, "sources")) {
		tuple := []any{get(v, "sceneId"), get(v, "messageId"), get(v, "senderId")}
		if o, ok := v.(*Object); ok {
			if ref, present := o.Get("eventRef"); present {
				tuple = append(tuple, []any{get(ref, "eventId"), get(ref, "revision"), get(ref, "deletionEpoch")})
			}
		}
		sources = append(sources, tuple)
	}
	sortTuples := func(values []any) error {
		keys := make(map[int]string, len(values))
		for i, v := range values {
			s, e := jsjson.Stringify(v)
			if e != nil {
				return e
			}
			keys[i] = s
		}
		type entry struct {
			key   string
			value any
		}
		entries := make([]entry, len(values))
		for i, v := range values {
			entries[i] = entry{keys[i], v}
		}
		sort.SliceStable(entries, func(i, j int) bool { return lessUTF16(entries[i].key, entries[j].key) })
		for i, e := range entries {
			values[i] = e.value
		}
		return nil
	}
	if e := sortTuples(sources); e != nil {
		return "", e
	}
	parents := append([]any{}, array(get(proof, "parents"))...)
	sort.SliceStable(parents, func(i, j int) bool { return lessUTF16(str(parents[i]), str(parents[j])) })
	var refs any
	if raw, present := proof.Get("parentReferences"); present {
		values := []any{}
		for _, ref := range array(raw) {
			values = append(values, []any{get(ref, "id"), get(ref, "version"), get(ref, "deletionEpoch")})
		}
		if e := sortTuples(values); e != nil {
			return "", e
		}
		refs = values
	}
	var witness any
	if w := get(proof, "subjectWitness"); w != nil && w != false {
		witness = []any{get(w, "kind"), get(w, "personId"), get(w, "sourceMessageId"), get(w, "mentionPartIndex")}
	}
	return jsjson.Stringify([]any{sources, parents, refs, get(proof, "invalidatedBy"), witness})
}
