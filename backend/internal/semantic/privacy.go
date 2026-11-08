package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"regexp"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Context struct {
	SceneID    string
	Simulation bool
}
type Requalification struct{ Before, After *jsjson.Object }

// PrivacyAuthority is bound to the caller's exact read/write scope. Callbacks
// must never obtain an independent writer or silently replace an exact ref.
type PrivacyAuthority struct {
	AssertReady   func() error
	Current       func(Context, *jsjson.Object) (*jsjson.Object, error)
	AnchorCurrent func(Context, *jsjson.Object) (*jsjson.Object, error)
	PartitionKey  func(Context, any) (string, error)
	EpisodeAnchor func(Context, *jsjson.Object, []any, string) (bool, error)
}

func privacyEqual(a, b any) bool {
	switch x := a.(type) {
	case *jsjson.Object:
		y, ok := b.(*jsjson.Object)
		if !ok {
			return false
		}
		if x == nil || y == nil {
			return x == y
		}
		if x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, ok := y.Get(m.Key)
			if !ok || !privacyEqual(m.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !privacyEqual(v, y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && ((math.IsNaN(x) && math.IsNaN(y)) || x == y && (x != 0 || math.Signbit(x) == math.Signbit(y)))
	default:
		return reflect.DeepEqual(a, b)
	}
}
func privacyHash(v any) (string, error) {
	raw, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}
func Fingerprint(r *jsjson.Object) (string, error) {
	v := []any{}
	for _, k := range []string{"id", "scope", "content", "kind", "epistemicKind", "occurredAt", "validFrom", "validUntil", "internalUse"} {
		v = append(v, scGet(r, k))
	}
	return privacyHash(v)
}
func DefinitionHash(r *jsjson.Object) (string, error) {
	v := []any{}
	for _, k := range []string{"id", "sceneId", "personId", "inputKey", "kind", "text", "selectedParentIds", "episodeAnchor", "episodeMembershipKey", "anchorQualifications", "hypothesis", "status", "admission", "createdAt", "applicableUntil"} {
		v = append(v, scGet(r, k))
	}
	return privacyHash(v)
}
func privacyIntersection(a, b any) []any {
	out := []any{}
	for _, p := range scArr(a) {
		for _, q := range scArr(b) {
			if scGet(p, "proofId") == scGet(q, "proofId") && privacyEqual(p, q) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
func privacyObject(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func (s *Store) transform(scene *string, mapper func(*jsjson.Object) (*jsjson.Object, error), qualify func(*jsjson.Object, *jsjson.Object) (*jsjson.Object, error)) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	rows, e := s.Rows(scene)
	if e != nil {
		return e
	}
	effects := []purgeEffect{}
	for _, before := range rows {
		after := before.Clone()
		missing := false
		for _, field := range []string{"parents", "anchorDependencies"} {
			mapped := []any{}
			for _, p := range scArr(scGet(before, field)) {
				next, e := mapper(scObject(p))
				if e != nil {
					return e
				}
				if next == nil {
					missing = true
				}
				mapped = append(mapped, next)
			}
			_ = after.Set(field, mapped)
		}
		if missing {
			after = nil
		} else if qualify != nil {
			after, e = qualify(before, after)
			if e != nil {
				return e
			}
		}
		if after != nil && privacyEqual(after, before) {
			continue
		}
		if after != nil {
			_ = after.Set("version", semNumber(before, "version")+1)
			if _, e = ValidateCandidate(after); e != nil {
				return e
			}
		}
		effects = append(effects, purgeEffect{before, after})
	}
	for _, effect := range effects {
		if e = s.Write(effect.after, effect.before, "privacy"); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) Erase(ctx Context, id string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if ctx.Simulation {
		return nil
	}
	v, e := s.Get(id)
	if e != nil {
		return e
	}
	if v != nil && scGet(v, "sceneId") == ctx.SceneID {
		return s.Write(nil, v, "privacy")
	}
	return nil
}
func (s *Store) PurgeSource(scene, message string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	root, e := privacyHash([]any{scene, message})
	if e != nil {
		return e
	}
	attempts, e := s.Attempts()
	if e != nil {
		return e
	}
	for _, a := range attempts {
		found := false
		for _, r := range scArr(scGet(a, "roots")) {
			found = found || r == root
		}
		if scGet(a, "sceneId") != scene || !found {
			continue
		}
		next := a.Clone()
		_ = next.Set("parents", []any{})
		_ = next.Set("roots", []any{})
		_ = next.Set("status", "abandoned")
		_ = next.Set("version", semNumber(a, "version")+1)
		if e = s.WriteAttempt(next, a); e != nil {
			return e
		}
	}
	return s.transform(nil, func(parent *jsjson.Object) (*jsjson.Object, error) {
		proofs := []any{}
		for _, p := range scArr(scGet(parent, "proofs")) {
			drop := false
			for _, source := range scArr(scGet(p, "sources")) {
				drop = drop || (scGet(source, "sceneId") == scene && scGet(source, "messageId") == message)
			}
			if !drop {
				proofs = append(proofs, p)
			}
		}
		if len(proofs) == 0 {
			return nil, nil
		}
		next := parent.Clone()
		_ = next.Set("proofs", proofs)
		return next, nil
	}, nil)
}
func (s *Store) PruneUnavailableProof(ctx Context, available func(*jsjson.Object, *jsjson.Object) (bool, error)) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if ctx.Simulation {
		return nil
	}
	return s.transform(&ctx.SceneID, func(parent *jsjson.Object) (*jsjson.Object, error) {
		proofs := []any{}
		for _, p := range scArr(scGet(parent, "proofs")) {
			ok, e := available(scObject(scGet(parent, "ref")), scObject(p))
			if e != nil {
				return nil, e
			}
			if ok {
				proofs = append(proofs, p)
			}
		}
		if len(proofs) == 0 {
			return nil, nil
		}
		next := parent.Clone()
		_ = next.Set("proofs", proofs)
		return next, nil
	}, nil)
}

var privacyPartition = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Store) Requalify(ctx Context, changes []Requalification, captured []*jsjson.Object, a PrivacyAuthority) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if ctx.Simulation {
		return nil
	}
	return s.transform(&ctx.SceneID, func(parent *jsjson.Object) (*jsjson.Object, error) {
		var change *Requalification
		for i := range changes {
			if privacyEqual(changes[i].Before, scGet(parent, "ref")) {
				change = &changes[i]
				break
			}
		}
		if change == nil {
			return parent, nil
		}
		if scGet(change.Before, "id") != scGet(change.After, "id") {
			return nil, errors.New("semantic_requalification_invalid")
		}
		var live *jsjson.Object
		var e error
		original := scHas(parent, "roots")
		if original {
			live, e = a.Current(ctx, change.After)
		} else {
			live, e = a.AnchorCurrent(ctx, change.After)
		}
		if e != nil {
			return nil, e
		}
		if live == nil {
			return parent, nil
		}
		if scHas(live, "rootCoverage") && scGet(live, "rootCoverage") != "complete" || scGet(live, "derived") == true {
			return parent, nil
		}
		fp, e := Fingerprint(scObject(scGet(live, "record")))
		if e != nil {
			return nil, e
		}
		if fp != scGet(parent, "fingerprint") {
			return parent, nil
		}
		proofs := privacyIntersection(scGet(parent, "proofs"), scGet(live, "proofs"))
		if len(proofs) == 0 {
			return parent, nil
		}
		next := parent.Clone()
		_ = next.Set("ref", change.After)
		_ = next.Set("proofs", proofs)
		if original {
			_ = next.Set("dependencyKey", scGet(live, "dependencyKey"))
			roots := []any{}
			for _, r := range scArr(scGet(parent, "roots")) {
				for _, q := range scArr(scGet(live, "roots")) {
					if r == q {
						roots = append(roots, r)
						break
					}
				}
			}
			_ = next.Set("roots", roots)
		}
		return next, nil
	}, func(before, after *jsjson.Object) (*jsjson.Object, error) {
		if scGet(before, "partitionKey") == nil {
			return after, nil
		}
		hash, e := DefinitionHash(before)
		if e != nil {
			return nil, e
		}
		var saved *jsjson.Object
		for _, q := range captured {
			if scGet(q, "id") == scGet(before, "id") && scGet(q, "inputKey") == scGet(before, "inputKey") && scGet(q, "partitionKey") == scGet(before, "partitionKey") && scGet(q, "definitionHash") == hash {
				saved = q
				break
			}
		}
		parents := scArr(scGet(before, "parents"))
		selections := []any{}
		for _, p := range parents {
			selections = append(selections, privacyObject("ref", scGet(p, "ref"), "dependencyKey", scGet(p, "dependencyKey")))
		}
		if saved == nil || !privacyEqual(scGet(saved, "parents"), selections) {
			return after, nil
		}
		changed := false
		for i, p := range scArr(scGet(after, "parents")) {
			changed = changed || !privacyEqual(scGet(p, "ref"), scGet(parents[i], "ref"))
		}
		if !changed {
			return after, nil
		}
		for _, p := range scArr(scGet(after, "parents")) {
			live, e := a.Current(ctx, scObject(scGet(p, "ref")))
			if e != nil {
				return nil, e
			}
			if live == nil || scGet(live, "dependencyKey") != scGet(p, "dependencyKey") || scGet(live, "rootCoverage") != "complete" || scGet(live, "derived") == true {
				return after, nil
			}
			fp, e := Fingerprint(scObject(scGet(live, "record")))
			if e != nil {
				return nil, e
			}
			if fp != scGet(p, "fingerprint") || len(privacyIntersection(scGet(p, "proofs"), scGet(live, "proofs"))) == 0 {
				return after, nil
			}
		}
		key, e := a.PartitionKey(ctx, scGet(before, "personId"))
		if e != nil {
			return nil, e
		}
		if privacyPartition.MatchString(key) {
			_ = after.Set("partitionKey", key)
		}
		return after, nil
	})
}

// CaptureQualification deliberately bypasses the feature flag, but still uses
// the normal exact parent, partition and episode checks. It admits no rejected
// candidates and captures definition identity before any pruning occurs.
func (s *Store) CaptureQualification(ctx Context, a PrivacyAuthority) ([]*jsjson.Object, error) {
	if e := a.AssertReady(); e != nil {
		return nil, e
	}
	out := []*jsjson.Object{}
	if ctx.Simulation {
		return out, nil
	}
	rows, e := s.Rows(&ctx.SceneID)
	if e != nil {
		return nil, e
	}
	return s.qualifyCandidates(ctx, a, rows, false)
}

func (s *Store) qualifyCandidates(ctx Context, a PrivacyAuthority, rows []*jsjson.Object, permitRejected bool) ([]*jsjson.Object, error) {
	out := []*jsjson.Object{}
	var e error
	partitions := map[any]string{}
	for _, row := range rows {
		if e = a.AssertReady(); e != nil {
			return nil, e
		}
		candidate, e := s.Get(scStr(scGet(row, "id")))
		if e != nil {
			return nil, e
		}
		if candidate == nil || scGet(candidate, "sceneId") != ctx.SceneID || (!permitRejected && scGet(candidate, "status") == "rejected") {
			continue
		}
		if scGet(candidate, "partitionKey") != nil {
			person := scGet(candidate, "personId")
			key, ok := partitions[person]
			if !ok {
				key, e = a.PartitionKey(ctx, person)
				if e != nil {
					return nil, e
				}
				partitions[person] = key
			}
			if key != scGet(candidate, "partitionKey") {
				continue
			}
		}
		valid := true
		for _, dep := range scArr(scGet(candidate, "anchorDependencies")) {
			ref := scObject(scGet(dep, "ref"))
			live, e := a.AnchorCurrent(ctx, ref)
			if e != nil {
				return nil, e
			}
			if live == nil {
				valid = false
				break
			}
			record := scObject(scGet(live, "record"))
			if scGet(record, "id") != scGet(ref, "id") || scGet(scGet(record, "scope"), "kind") != "scene" || scGet(scGet(record, "scope"), "sceneId") != ctx.SceneID {
				valid = false
				break
			}
			fp, e := Fingerprint(record)
			if e != nil {
				return nil, e
			}
			if fp != scGet(dep, "fingerprint") || len(privacyIntersection(scGet(dep, "proofs"), scGet(live, "proofs"))) == 0 {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		parents := []any{}
		refs := []any{}
		for _, parent := range scArr(scGet(candidate, "parents")) {
			ref := scObject(scGet(parent, "ref"))
			live, e := a.Current(ctx, ref)
			if e != nil {
				return nil, e
			}
			if live == nil {
				valid = false
				break
			}
			record := scObject(scGet(live, "record"))
			roots := scArr(scGet(live, "roots"))
			if scGet(record, "id") != scGet(ref, "id") || scGet(scGet(record, "scope"), "kind") != "scene" || scGet(scGet(record, "scope"), "sceneId") != ctx.SceneID || scGet(live, "derived") == true || scGet(record, "internalUse") == true || scGet(live, "dependencyKey") != scGet(parent, "dependencyKey") || scGet(live, "rootCoverage") != "complete" || len(roots) == 0 || len(roots) > 128 || len(scArr(scGet(live, "proofs"))) == 0 {
				valid = false
				break
			}
			for _, root := range roots {
				if !privacyPartition.MatchString(scStr(root)) {
					valid = false
					break
				}
			}
			if !valid {
				break
			}
			fp, e := Fingerprint(record)
			if e != nil {
				return nil, e
			}
			if fp != scGet(parent, "fingerprint") || len(privacyIntersection(scGet(parent, "proofs"), scGet(live, "proofs"))) == 0 {
				valid = false
				break
			}
			parents = append(parents, privacyObject("ref", ref.Clone(), "dependencyKey", scGet(parent, "dependencyKey")))
			refs = append(refs, ref)
		}
		if !valid {
			continue
		}
		if scHas(candidate, "episodeAnchor") {
			ok, e := a.EpisodeAnchor(ctx, scObject(scGet(candidate, "episodeAnchor")), refs, scStr(scGet(candidate, "episodeMembershipKey")))
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
		}
		hash, e := DefinitionHash(candidate)
		if e != nil {
			return nil, e
		}
		out = append(out, privacyObject("id", scGet(candidate, "id"), "inputKey", scGet(candidate, "inputKey"), "definitionHash", hash, "partitionKey", scGet(candidate, "partitionKey"), "parents", parents))
	}
	return out, nil
}
