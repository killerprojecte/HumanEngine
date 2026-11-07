// Package workingset persists optional attention metadata. It never grants
// content access, mutates evidence, performs model work, or replays old sources.
package workingset

import (
	"errors"
	"math"
	"reflect"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Reference = recovery.WorkingSetReference
type Selection = recovery.WorkingSetSelection
type PinPolicy = recovery.WorkingSetPinPolicy
type Entity struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Context struct {
	SceneID    string `json:"sceneId"`
	Simulation bool   `json:"simulation"`
}
type Snapshot struct {
	Ref           Reference `json:"ref"`
	DependencyKey string    `json:"dependencyKey"`
	SceneID       string    `json:"sceneId"`
	PersonID      *string   `json:"personId,omitempty"`
	RecordedAt    *float64  `json:"recordedAt"`
	EpistemicKind *string   `json:"epistemicKind,omitempty"`
	Protection    *string   `json:"protection"`
}

func (s Snapshot) Selection() Selection { return Selection{Ref: s.Ref, DependencyKey: s.DependencyKey} }

type Item struct {
	SchemaVersion        int      `json:"schemaVersion"`
	Snapshot             Snapshot `json:"snapshot"`
	MetadataRevision     float64  `json:"metadataRevision"`
	ContextSelectedCount float64  `json:"contextSelectedCount"`
	LastSelectedAt       *float64 `json:"lastSelectedAt"`
	ProjectedAt          float64  `json:"projectedAt"`
}
type JobLease struct {
	Token     string  `json:"token"`
	ExpiresAt float64 `json:"expiresAt"`
}
type Job struct {
	SchemaVersion int       `json:"schemaVersion"`
	Snapshot      Snapshot  `json:"snapshot"`
	Generation    string    `json:"generation"`
	EnqueuedAt    float64   `json:"enqueuedAt"`
	Lease         *JobLease `json:"lease"`
}
type Lease struct {
	SceneID       string    `json:"sceneId"`
	Ref           Reference `json:"ref"`
	DependencyKey string    `json:"dependencyKey"`
	Generation    string    `json:"generation"`
	Token         string    `json:"token"`
	ExpiresAt     float64   `json:"expiresAt"`
}

func (l Lease) Selection() Selection { return Selection{Ref: l.Ref, DependencyKey: l.DependencyKey} }

type Receipt struct {
	SchemaVersion int       `json:"schemaVersion"`
	SceneID       string    `json:"sceneId"`
	TurnID        string    `json:"turnId"`
	Selection     Selection `json:"selection"`
	SelectedAt    float64   `json:"selectedAt"`
}
type ViewItem struct {
	Ref                  Reference `json:"ref"`
	DependencyKey        string    `json:"dependencyKey"`
	State                string    `json:"state"`
	ReviewDue            bool      `json:"reviewDue"`
	Pinned               bool      `json:"pinned"`
	PinIntent            bool      `json:"pinIntent"`
	PinRevision          float64   `json:"pinRevision"`
	PinStatus            string    `json:"pinStatus"`
	PolicyCoverage       string    `json:"policyCoverage"`
	MetadataRevision     float64   `json:"metadataRevision"`
	ContextSelectedCount float64   `json:"contextSelectedCount"`
	LastSelectedAt       *float64  `json:"lastSelectedAt"`
	Protection           *string   `json:"protection"`
}

const MaxSafe = 9007199254740991.0

func safe(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= MaxSafe && math.Trunc(n) == n
}
func id(s string) bool         { n, e := jsjson.CodePointCount(s); return e == nil && n >= 1 && n <= 512 }
func explicitID(s string) bool { n, e := jsjson.UTF16Len(s); return e == nil && n >= 1 && n <= 512 }

var dependency = regexp.MustCompile(`^[a-f0-9]{64}$`)
var uuid = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`)

func invalid() error { return errors.New("working_set_storage_invalid") }
func ValidateReference(r Reference) error {
	if !id(r.ID) || !safe(r.Version) || r.Version < 1 || !safe(r.DeletionEpoch) {
		return invalid()
	}
	switch r.Kind {
	case "assertion":
		if r.MutationVersion != nil {
			return invalid()
		}
	case "commitment":
		if r.MutationVersion == nil || !safe(*r.MutationVersion) || *r.MutationVersion < 1 {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}
func ValidateSelection(s Selection) error {
	if e := ValidateReference(s.Ref); e != nil {
		return e
	}
	if !dependency.MatchString(s.DependencyKey) {
		return invalid()
	}
	return nil
}
func ValidateSnapshot(s Snapshot) error {
	if e := ValidateSelection(s.Selection()); e != nil {
		return e
	}
	if !id(s.SceneID) || (s.PersonID != nil && !id(*s.PersonID)) || (s.RecordedAt != nil && !safe(*s.RecordedAt)) {
		return invalid()
	}
	if s.EpistemicKind != nil && !oneOf(*s.EpistemicKind, "explicit", "reported", "observed", "inferred", "hypothesis") {
		return invalid()
	}
	if s.Protection != nil && !oneOf(*s.Protection, "open_commitment", "needs_review_commitment", "unresolved_commitment_coverage") {
		return invalid()
	}
	return nil
}
func oneOf(s string, vs ...string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}
	return false
}
func validate(v any) error {
	switch x := v.(type) {
	case Reference:
		return ValidateReference(x)
	case Selection:
		return ValidateSelection(x)
	case Snapshot:
		return ValidateSnapshot(x)
	case PinPolicy:
		if x.SchemaVersion != 1 || !id(x.SceneID) || !safe(x.PolicyRevision) || x.PolicyRevision < 1 || (x.Terminal && x.Pinned) {
			return invalid()
		}
		return ValidateSelection(x.Selection)
	case Item:
		if x.SchemaVersion != 1 || !safe(x.MetadataRevision) || x.MetadataRevision < 1 || !safe(x.ContextSelectedCount) || (x.LastSelectedAt != nil && !safe(*x.LastSelectedAt)) || !safe(x.ProjectedAt) {
			return invalid()
		}
		return ValidateSnapshot(x.Snapshot)
	case Job:
		if x.SchemaVersion != 1 || !uuid.MatchString(x.Generation) || !safe(x.EnqueuedAt) || (x.Lease != nil && (!uuid.MatchString(x.Lease.Token) || !safe(x.Lease.ExpiresAt))) {
			return invalid()
		}
		return ValidateSnapshot(x.Snapshot)
	case Receipt:
		if x.SchemaVersion != 1 || !id(x.SceneID) || !id(x.TurnID) || !safe(x.SelectedAt) {
			return invalid()
		}
		return ValidateSelection(x.Selection)
	default:
		return invalid()
	}
}

// Decode applies strict source schemas before returning typed values. It never
// passes JS strings through encoding/json. Encode rebuilds schema field order.
func Decode[T any](raw string) (out T, err error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return out, invalid()
	}
	if e = decodeValue(v, reflect.ValueOf(&out).Elem()); e != nil {
		return out, e
	}
	return out, validate(out)
}
func Encode(v any) (string, error) {
	if e := validate(v); e != nil {
		return "", e
	}
	return JSON(v)
}

// JSON serializes our typed result records in declaration (source) order, with
// exact JS number and WTF-8 string semantics. It is also usable by fixture tools.
func JSON(v any) (string, error) {
	x, e := encodeValue(reflect.ValueOf(v))
	if e != nil {
		return "", e
	}
	return jsjson.Stringify(x)
}
func encodeValue(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		return encodeValue(v.Elem())
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Float64:
		return v.Float(), nil
	case reflect.Int, reflect.Int64:
		return float64(v.Int()), nil
	case reflect.Slice, reflect.Array:
		a := []any{}
		for i := 0; i < v.Len(); i++ {
			x, e := encodeValue(v.Index(i))
			if e != nil {
				return nil, e
			}
			a = append(a, x)
		}
		return a, nil
	case reflect.Struct:
		o := new(jsjson.Object)
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			value := v.Field(i)
			if len(tag) > 1 && tag[1] == "omitempty" && value.IsZero() {
				continue
			}
			x, e := encodeValue(value)
			if e != nil {
				return nil, e
			}
			if e = o.Set(name, x); e != nil {
				return nil, e
			}
		}
		return o, nil
	}
	return nil, invalid()
}
func decodeValue(raw any, v reflect.Value) error {
	if v.Kind() == reflect.Pointer {
		if raw == nil {
			return nil
		}
		v.Set(reflect.New(v.Type().Elem()))
		return decodeValue(raw, v.Elem())
	}
	switch v.Kind() {
	case reflect.Slice:
		a, ok := raw.([]any)
		if !ok {
			return invalid()
		}
		v.Set(reflect.MakeSlice(v.Type(), len(a), len(a)))
		for i, x := range a {
			if e := decodeValue(x, v.Index(i)); e != nil {
				return e
			}
		}
		return nil
	case reflect.Struct:
		o, ok := raw.(*jsjson.Object)
		if !ok {
			return invalid()
		}
		t := v.Type()
		known := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("json"), ",")
			name := tag[0]
			known[name] = true
			x, found := o.Get(name)
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if !found {
				if optional {
					continue
				}
				return invalid()
			}
			if optional && x == nil {
				return invalid()
			}
			if e := decodeValue(x, v.Field(i)); e != nil {
				return e
			}
		}
		for _, k := range o.Keys() {
			if !known[k] {
				return invalid()
			}
		}
		return nil
	case reflect.String:
		x, ok := raw.(string)
		if !ok {
			return invalid()
		}
		v.SetString(x)
	case reflect.Float64:
		x, ok := raw.(float64)
		if !ok {
			return invalid()
		}
		v.SetFloat(x)
	case reflect.Int:
		x, ok := raw.(float64)
		if !ok || !safe(x) || x > float64(math.MaxInt) {
			return invalid()
		}
		v.SetInt(int64(x))
	case reflect.Bool:
		x, ok := raw.(bool)
		if !ok {
			return invalid()
		}
		v.SetBool(x)
	default:
		return invalid()
	}
	return nil
}
