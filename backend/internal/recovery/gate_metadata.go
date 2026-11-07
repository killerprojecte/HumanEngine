package recovery

import (
	"errors"
	"math"
	"path/filepath"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Decode source metadata with JSON.parse's exact-key, UTF-16 property identity,
// and duplicate-last-value semantics. Struct decoding folds key case and merges
// duplicate objects; encoding/json map keys collapse distinct lone surrogates.
// Retain the parsed object so reader upgrades match source JSON.stringify too.
func decodeGate(value string) (g Gate, raw *jsjson.Object, err error) {
	invalid := errors.New("recovery_gate_invalid")
	parsed, parseErr := jsjson.Parse(value)
	raw, ok := parsed.(*jsjson.Object)
	if parseErr != nil || !ok {
		return g, nil, invalid
	}
	hv, _ := raw.Get("head")
	head, ok := hv.(*jsjson.Object)
	if !ok {
		return g, raw, invalid
	}
	stringField := func(object *jsjson.Object, key string, target *string) bool {
		value, _ := object.Get(key)
		var ok bool
		*target, ok = value.(string)
		return ok
	}
	sv, _ := head.Get("sequence")
	seq, sequenceOK := sv.(float64)
	rv, _ := raw.Get("minimumReader")
	reader, readerOK := rv.(float64)
	if !sequenceOK || !readerOK || !stringField(head, "instanceId", &g.Head.InstanceID) ||
		!stringField(head, "hash", &g.Head.Hash) || !stringField(raw, "authority", &g.Authority) || !stringField(raw, "state", &g.State) {
		return g, raw, invalid
	}
	if seq < 0 || seq > 9007199254740991 || math.Trunc(seq) != seq || reader < 1 || reader > 9007199254740991 || math.Trunc(reader) != reader {
		return g, raw, invalid
	}
	g.Head.Sequence, g.MinimumReader = int64(seq), int64(reader)
	if (g.State != "quarantine" && g.State != "ready") || !filepath.IsAbs(g.Authority) || filepath.Clean(g.Authority) != g.Authority ||
		!authorityInstancePattern.MatchString(g.Head.InstanceID) || !hashPattern.MatchString(g.Head.Hash) {
		return g, raw, invalid
	}
	return g, raw, nil
}
