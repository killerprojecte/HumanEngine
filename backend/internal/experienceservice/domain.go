// Package experienceservice owns fixed, low-priority experience policies.
package experienceservice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type Context struct {
	SceneID    string `json:"sceneId"`
	Simulation bool   `json:"simulation"`
}
type Reference struct {
	raw     *jsjson.Object
	ID      string `json:"id"`
	Version int64  `json:"version"`
}
type Source struct {
	eventRefRaw *rawReference
	extraFields *jsjson.Object
	MessageID   string            `json:"messageId"`
	SenderID    string            `json:"senderId"`
	EventRef    *sourceevents.Ref `json:"-"`
}
type Target struct {
	eventRefRaw *rawReference
	extraFields *jsjson.Object
	MessageID   string            `json:"messageId"`
	TurnID      string            `json:"turnId,omitempty"`
	EventRef    *sourceevents.Ref `json:"-"`
}
type Feedback struct {
	extraFields *jsjson.Object
	Source      Source     `json:"source"`
	Target      Target     `json:"target"`
	Code        string     `json:"code"`
	PolicyRef   *Reference `json:"policyRef,omitempty"`
	ObservedAt  string     `json:"observedAt"`
}
type Outcome struct {
	presentation *presentationMaterial
	extraFields  *jsjson.Object
	Feedback
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	SceneID       string `json:"sceneId"`
	RootID        string `json:"rootId"`
	RootFamily    string `json:"rootFamily"`
	SnapshotBound bool   `json:"snapshotBound,omitempty"`
}
type Condition struct {
	Kind     string `json:"kind"`
	PersonID string `json:"personId,omitempty"`
	raw      *jsjson.Object
}
type Policy struct {
	presentation       *presentationMaterial
	extraFields        *jsjson.Object
	ID                 string    `json:"id"`
	Version            int64     `json:"version"`
	Hash               string    `json:"hash"`
	SceneID            string    `json:"sceneId"`
	Action             string    `json:"action"`
	Condition          Condition `json:"condition"`
	Status             string    `json:"status"`
	SupportIDs         []string  `json:"supportIds"`
	CounterexampleIDs  []string  `json:"counterexampleIds"`
	GenerationRoots    []string  `json:"generationRoots"`
	GenerationFamilies []string  `json:"generationFamilies"`
	CreatedAt          string    `json:"createdAt"`
	ExpiresAt          string    `json:"expiresAt"`
	Reason             string    `json:"reason,omitempty"`
	ValidationID       string    `json:"validationId,omitempty"`
	ActivationBasis    string    `json:"activationBasis,omitempty"`
	unknownFields      bool
}
type Receipt struct {
	presentation       *presentationMaterial
	extraFields        *jsjson.Object
	CandidateID        string   `json:"candidateId"`
	CandidateVersion   int64    `json:"candidateVersion"`
	CandidateHash      string   `json:"candidateHash"`
	Tier               string   `json:"tier"`
	RuleVersion        string   `json:"ruleVersion"`
	RuleHash           string   `json:"ruleHash"`
	DatasetVersion     string   `json:"datasetVersion"`
	DatasetHash        string   `json:"datasetHash"`
	GenerationRoots    []string `json:"generationRoots"`
	GenerationFamilies []string `json:"generationFamilies"`
	HeldoutRoots       []string `json:"heldoutRoots"`
	HeldoutFamilies    []string `json:"heldoutFamilies"`
	Checks             []string `json:"checks"`
	Failures           []string `json:"failures"`
	Passed             bool     `json:"passed"`
}
type Hint struct {
	extraFields          *jsjson.Object
	ID                   string    `json:"id"`
	Version              int64     `json:"version"`
	Hash                 string    `json:"hash"`
	Action               string    `json:"action"`
	Condition            Condition `json:"condition"`
	ExpiresAt            string    `json:"expiresAt"`
	Tier                 string    `json:"tier"`
	EligibilityBasis     string    `json:"eligibilityBasis,omitempty"`
	FeedbackRevision     *int64    `json:"feedbackRevision,omitempty"`
	TrialVersion         *int64    `json:"trialVersion,omitempty"`
	TrialExpiresAt       string    `json:"trialExpiresAt,omitempty"`
	ReportedHelpfulCount *int      `json:"reportedHelpfulCount,omitempty"`
}
type Use struct {
	extraFields     *jsjson.Object
	TurnID          string    `json:"turnId"`
	PolicyRef       Reference `json:"policyRef"`
	Kind            string    `json:"kind"`
	Operation       string    `json:"operation,omitempty"`
	TargetMessageID string    `json:"targetMessageId,omitempty"`
}
type Proposal struct {
	Action      string      `json:"action"`
	Condition   Condition   `json:"condition"`
	OutcomeRefs []Reference `json:"outcomeRefs"`
}

const ExperienceTTLMS int64 = 7 * 24 * 60 * 60 * 1000
const maxSafeVersion int64 = 9007199254740991

func object(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func stringsValue(ss []string) []any {
	a := make([]any, len(ss))
	for i, s := range ss {
		a[i] = s
	}
	return a
}
func digestValue(v any) string {
	b, e := jsjson.Marshal(v)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Key(parts ...string) string             { return digestValue(stringsValue(parts)) }
func SourceKey(scene, message string) string { return Key("source", scene, message) }
func sortedValues(ss []string) []any {
	a := append([]string{}, ss...)
	sort.SliceStable(a, func(i, j int) bool {
		x, _ := jsjson.UTF16(a[i])
		y, _ := jsjson.UTF16(a[j])
		for k := 0; k < len(x) && k < len(y); k++ {
			if x[k] != y[k] {
				return x[k] < y[k]
			}
		}
		return len(x) < len(y)
	})
	return stringsValue(a)
}
func (c Condition) value() *jsjson.Object {
	if c.raw != nil {
		return c.raw
	}
	if c.Kind == "plan_question" && c.PersonID == "" {
		return object("kind", c.Kind)
	}
	return object("kind", c.Kind, "personId", c.PersonID)
}
func (c Condition) MarshalJSON() ([]byte, error) { return jsjson.Marshal(c.value()) }
func (c *Condition) UnmarshalJSON(b []byte) error {
	v, e := jsjson.Parse(string(b))
	if e != nil {
		return e
	}
	o, ok := v.(*jsjson.Object)
	if !ok {
		return errors.New("experience_condition_invalid")
	}
	c.raw = o
	c.Kind, _ = getValue(o, "kind").(string)
	c.PersonID, _ = getValue(o, "personId").(string)
	return nil
}
func getValue(o *jsjson.Object, k string) any { v, _ := o.Get(k); return v }
func CandidateHash(c Policy) string {
	o := object("sceneId", c.SceneID, "action", c.Action, "condition", c.Condition.value(), "supportIds", sortedValues(c.SupportIDs), "counterexampleIds", sortedValues(c.CounterexampleIDs), "generationRoots", sortedValues(c.GenerationRoots), "generationFamilies", sortedValues(c.GenerationFamilies))
	for _, key := range []string{"createdAt", "expiresAt"} {
		if v, ok := policyScalarMaterial(c, key); ok {
			check(o.Set(key, cloneReferenceValue(v)))
		}
	}
	return digestValue(o)
}

var timezoneSuffix = regexp.MustCompile(`(?:Z|[+-]\d\d:\d\d)$`)

func dateMillis(s string) (int64, bool) {
	p, _ := jsdate.New("UTC")
	n, ok, e := p.ParseMillis(s)
	return n, ok && e == nil
}
func isoMillis(n int64) string { return time.UnixMilli(n).UTC().Format("2006-01-02T15:04:05.000Z") }
func member(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func CreateOutcome(ctx Context, input Feedback, id string) (*Outcome, error) {
	if !member(input.Code, "request_exact_person_lookup", "request_verify_plan_qualifiers", "request_conclusion_first", "counterexample", "withdraw_instruction", "user_reported_helpful", "withdraw_person_instruction", "withdraw_plan_instruction") || input.Source.MessageID == "" || input.Source.SenderID == "" || input.Target.MessageID == "" || !jsdate.FiniteDateParse(input.ObservedAt) || !timezoneSuffix.MatchString(input.ObservedAt) {
		return nil, errors.New("experience_invalid_feedback")
	}
	if member(input.Code, "counterexample", "withdraw_instruction", "user_reported_helpful", "withdraw_person_instruction", "withdraw_plan_instruction") && (input.PolicyRef == nil || input.PolicyRef.Version < 1 || input.PolicyRef.Version > maxSafeVersion) {
		return nil, errors.New("experience_target_required")
	}
	if input.PolicyRef != nil {
		v := *input.PolicyRef
		input.PolicyRef = &v
	}
	if input.Source.EventRef != nil {
		v := *input.Source.EventRef
		input.Source.EventRef = &v
	}
	if input.Target.EventRef != nil {
		v := *input.Target.EventRef
		input.Target.EventRef = &v
	}
	return &Outcome{Feedback: input, ID: id, Version: 1, SceneID: ctx.SceneID, RootID: Key(ctx.SceneID, input.Source.MessageID, input.Target.MessageID), RootFamily: "feedback:" + input.Code}, nil
}
func CandidateFromOutcome(o Outcome, id string, now int64) (*Policy, error) {
	if member(o.Code, "counterexample", "withdraw_instruction", "user_reported_helpful", "withdraw_person_instruction", "withdraw_plan_instruction") {
		return nil, errors.New("experience_counterexample_not_candidate")
	}
	action := "conclusion_before_detail"
	condition := Condition{Kind: "response_style", PersonID: o.Source.SenderID}
	if o.Code == "request_exact_person_lookup" {
		action = "exact_person_before_semantic"
		condition.Kind = "person_question"
	} else if o.Code == "request_verify_plan_qualifiers" {
		action = "verify_plan_qualifiers"
		condition = Condition{Kind: "plan_question"}
	}
	p := &Policy{ID: id, Version: 1, SceneID: o.SceneID, Action: action, Condition: condition, Status: "candidate", SupportIDs: []string{o.ID}, CounterexampleIDs: []string{}, GenerationRoots: []string{SourceKey(o.SceneID, o.Source.MessageID)}, GenerationFamilies: []string{o.RootFamily}, CreatedAt: isoMillis(now), ExpiresAt: isoMillis(now + ExperienceTTLMS)}
	p.Hash = CandidateHash(*p)
	return p, nil
}
func ValidMechanicalReceipt(c Policy, r Receipt) bool {
	if r.CandidateID != c.ID || r.CandidateVersion != c.Version || r.CandidateHash != c.Hash || CandidateHash(c) != c.Hash || r.Tier != "mechanical_only" || !r.Passed || len(r.Failures) > 0 || len(r.Checks) == 0 || r.RuleHash == "" || r.RuleVersion == "" || r.DatasetVersion == "" || r.DatasetHash == "" || len(r.HeldoutRoots) == 0 || len(r.HeldoutFamilies) == 0 {
		return false
	}
	equal := func(a, b []string) bool {
		x, _ := jsjson.Stringify(sortedValues(a))
		y, _ := jsjson.Stringify(sortedValues(b))
		return x == y
	}
	return equal(r.GenerationRoots, c.GenerationRoots) && equal(r.GenerationFamilies, c.GenerationFamilies) && disjoint(r.HeldoutRoots, c.GenerationRoots) && disjoint(r.HeldoutFamilies, c.GenerationFamilies)
}
func disjoint(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return false
			}
		}
	}
	return true
}

// Domain JSON uses the source's UTF-16 preserving representation. The generic
// encoding/json decoder replaces lone surrogates and cannot be used for hashes
// or persisted source identifiers.
var conditionType = reflect.TypeOf(Condition{})
var refType = reflect.TypeOf(sourceevents.Ref{})
var policyRefType = reflect.TypeOf(Reference{})

func domainExtras(v any) *jsjson.Object {
	switch x := v.(type) {
	case Source:
		return x.extraFields
	case Target:
		return x.extraFields
	case Feedback:
		return x.extraFields
	case Outcome:
		return x.extraFields
	case Policy:
		return x.extraFields
	case Receipt:
		return x.extraFields
	case Hint:
		return x.extraFields
	case Use:
		return x.extraFields
	}
	return nil
}
func setDomainExtras(v any, o *jsjson.Object) {
	switch x := v.(type) {
	case *Source:
		x.extraFields = o
	case *Target:
		x.extraFields = o
	case *Feedback:
		x.extraFields = o
	case *Outcome:
		x.extraFields = o
	case *Policy:
		x.extraFields = o
	case *Receipt:
		x.extraFields = o
	case *Hint:
		x.extraFields = o
	case *Use:
		x.extraFields = o
	}
}
func domainFieldNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if f.Anonymous {
			for k := range domainFieldNames(f.Type) {
				names[k] = true
			}
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Name == "SourceEventRef" {
			name = "sourceEventRef"
		} else if f.Name == "EventRef" {
			name = "eventRef"
		}
		if name != "-" {
			if name == "" {
				name = f.Name
			}
			names[name] = true
		}
	}
	return names
}

func domainValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return domainValue(v.Elem())
	}
	if v.Type() == policyRefType {
		r := v.Interface().(Reference)
		o := new(jsjson.Object)
		if r.raw != nil {
			o = r.raw.Clone()
		}
		_ = o.Set("id", r.ID)
		_ = o.Set("version", float64(r.Version))
		return o
	}
	if v.Type() == conditionType {
		return v.Interface().(Condition).value()
	}
	if v.Type() == refType {
		r := v.Interface().(sourceevents.Ref)
		return object("eventId", r.EventID, "revision", float64(r.Revision), "deletionEpoch", float64(r.DeletionEpoch))
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int64:
		return float64(v.Int())
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		a := make([]any, v.Len())
		for i := range a {
			a[i] = domainValue(v.Index(i))
		}
		return a
	case reflect.Struct:
		o := new(jsjson.Object)
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			if f.Anonymous {
				a, _ := domainValue(v.Field(i)).(*jsjson.Object)
				for _, k := range a.Keys() {
					_ = o.Set(k, getValue(a, k))
				}
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if f.Name == "SourceEventRef" {
				name = "sourceEventRef"
				if v.Field(i).IsNil() && eventRefRaw(v.Interface()) == nil {
					continue
				}
			} else if f.Name == "EventRef" {
				name = "eventRef"
				if v.Field(i).IsNil() && eventRefRaw(v.Interface()) == nil {
					continue
				}
			} else if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if len(tag) > 1 && tag[1] == "omitempty" && v.Field(i).IsZero() {
				continue
			}
			if f.Name == "EventRef" || f.Name == "SourceEventRef" {
				_ = o.Set(name, eventRefValue(v.Field(i).Interface().(*sourceevents.Ref), eventRefRaw(v.Interface())))
			} else {
				_ = o.Set(name, domainValue(v.Field(i)))
			}
		}
		if extra := domainExtras(v.Interface()); extra != nil {
			for _, k := range extra.Keys() {
				_ = o.Set(k, getValue(extra, k))
			}
		}
		return o
	}
	panic("unsupported experience domain field: " + v.Type().String())
}
func decodeDomain(v any, d reflect.Value) error {
	if d.Kind() == reflect.Pointer {
		if v == nil {
			d.SetZero()
			return nil
		}
		d.Set(reflect.New(d.Type().Elem()))
		return decodeDomain(v, d.Elem())
	}
	if d.Type() == policyRefType {
		o, ok := v.(*jsjson.Object)
		if !ok {
			return errors.New("experience_reference_invalid")
		}
		id, ok := getValue(o, "id").(string)
		if !ok {
			return errors.New("experience_reference_invalid")
		}
		n, ok := getValue(o, "version").(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < -9007199254740991 || n > 9007199254740991 {
			return errors.New("experience_reference_invalid")
		}
		d.Set(reflect.ValueOf(Reference{ID: id, Version: int64(n), raw: o.Clone()}))
		return nil
	}
	if d.Type() == conditionType {
		o, ok := v.(*jsjson.Object)
		if !ok {
			return errors.New("experience_condition_invalid")
		}
		d.Set(reflect.ValueOf(Condition{Kind: stringValue(getValue(o, "kind")), PersonID: stringValue(getValue(o, "personId")), raw: o}))
		return nil
	}
	if d.Type() == refType {
		o, ok := v.(*jsjson.Object)
		if !ok {
			return errors.New("experience_ref_invalid")
		}
		var r sourceevents.Ref
		id, ok := getValue(o, "eventId").(string)
		if !ok || !validID(id) {
			return errors.New("experience_ref_invalid")
		}
		r.EventID = id
		n, ok := getValue(o, "revision").(float64)
		if !ok || !safeRefNumber(n, 1) {
			return errors.New("experience_ref_invalid")
		}
		r.Revision = int64(n)
		n, ok = getValue(o, "deletionEpoch").(float64)
		if !ok || !safeRefNumber(n, 0) {
			return errors.New("experience_ref_invalid")
		}
		r.DeletionEpoch = int64(n)
		d.Set(reflect.ValueOf(r))
		return nil
	}
	switch d.Kind() {
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return errors.New("experience_string_invalid")
		}
		d.SetString(s)
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return errors.New("experience_boolean_invalid")
		}
		d.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, ok := v.(float64)
		if !ok || float64(int64(n)) != n {
			return errors.New("experience_number_invalid")
		}
		d.SetInt(int64(n))
	case reflect.Slice:
		if v == nil {
			d.SetZero()
			return nil
		}
		a, ok := v.([]any)
		if !ok {
			return errors.New("experience_array_invalid")
		}
		d.Set(reflect.MakeSlice(d.Type(), len(a), len(a)))
		for i, item := range a {
			if e := decodeDomain(item, d.Index(i)); e != nil {
				return e
			}
		}
	case reflect.Struct:
		o, ok := v.(*jsjson.Object)
		if !ok {
			return errors.New("experience_object_invalid")
		}
		t := d.Type()
		for i := 0; i < d.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			if f.Anonymous {
				if e := decodeDomain(o, d.Field(i)); e != nil {
					return e
				}
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.Name == "SourceEventRef" {
				name = "sourceEventRef"
			} else if f.Name == "EventRef" {
				name = "eventRef"
			} else if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, has := o.Get(name)
			if has {
				// Date.parse consumes scalar JSON values through JavaScript string coercion;
				// preserve their actual JSON material for hash/persistence/schema projection.
				if _, policy := d.Addr().Interface().(*Policy); policy {
					if name == "createdAt" || name == "expiresAt" {
						switch value.(type) {
						case nil, bool, float64:
							text, e := profileidentity.StringValue(value)
							if e != nil {
								return e
							}
							d.Field(i).SetString(text)
							continue
						}
					}
					if name == "activationBasis" && (value == nil || value == false) {
						d.Field(i).SetString("")
						continue
					}
				}
				if f.Name == "EventRef" || f.Name == "SourceEventRef" {
					// Presence and arbitrary JSON material are independent of typed
					// authority. Null is present; malformed values never get coerced.
					setEventRefRaw(d.Addr().Interface(), &rawReference{cloneReferenceValue(value)})
					d.Field(i).SetZero()
					if ref, e := sourceevents.ParseRef(value); e == nil {
						d.Field(i).Set(reflect.ValueOf(&ref))
					}
					continue
				}
				if e := decodeDomain(value, d.Field(i)); e != nil {
					return fmt.Errorf("%s: %w", name, e)
				}
			}
		}
		known := domainFieldNames(t)
		extra := new(jsjson.Object)
		for _, k := range o.Keys() {
			if !known[k] {
				_ = extra.Set(k, getValue(o, k))
			}
		}
		setDomainExtras(d.Addr().Interface(), extra)
		retainPresentation(d.Addr().Interface(), o)
	default:
		return fmt.Errorf("unsupported experience domain decode: %s", d.Type())
	}
	return nil
}
func stringValue(v any) string            { s, _ := v.(string); return s }
func marshalDomain(v any) ([]byte, error) { return jsjson.Marshal(domainValue(reflect.ValueOf(v))) }
func unmarshalDomain(b []byte, p any) error {
	v, e := jsjson.Parse(string(b))
	if e != nil {
		return e
	}
	return decodeDomain(v, reflect.ValueOf(p).Elem())
}
func (s Source) MarshalJSON() ([]byte, error)    { return marshalDomain(s) }
func (s *Source) UnmarshalJSON(b []byte) error   { return unmarshalDomain(b, s) }
func (s Target) MarshalJSON() ([]byte, error)    { return marshalDomain(s) }
func (s *Target) UnmarshalJSON(b []byte) error   { return unmarshalDomain(b, s) }
func (s Feedback) MarshalJSON() ([]byte, error)  { return marshalDomain(s) }
func (s *Feedback) UnmarshalJSON(b []byte) error { return unmarshalDomain(b, s) }
func (s Outcome) MarshalJSON() ([]byte, error)   { return marshalDomain(s) }
func (s *Outcome) UnmarshalJSON(b []byte) error  { return unmarshalDomain(b, s) }
func (s Policy) MarshalJSON() ([]byte, error)    { return marshalDomain(s) }
func (s *Policy) UnmarshalJSON(b []byte) error {
	if e := unmarshalDomain(b, s); e != nil {
		return e
	}
	v, _ := jsjson.Parse(string(b))
	o := v.(*jsjson.Object)
	allowed := map[string]bool{}
	t := reflect.TypeOf(*s)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath == "" {
			allowed[strings.Split(f.Tag.Get("json"), ",")[0]] = true
		}
	}
	for _, k := range o.Keys() {
		if !allowed[k] {
			s.unknownFields = true
		}
	}
	return nil
}
func (s Receipt) MarshalJSON() ([]byte, error)    { return marshalDomain(s) }
func (s *Receipt) UnmarshalJSON(b []byte) error   { return unmarshalDomain(b, s) }
func (s Hint) MarshalJSON() ([]byte, error)       { return marshalDomain(s) }
func (s *Hint) UnmarshalJSON(b []byte) error      { return unmarshalDomain(b, s) }
func (s Use) MarshalJSON() ([]byte, error)        { return marshalDomain(s) }
func (s *Use) UnmarshalJSON(b []byte) error       { return unmarshalDomain(b, s) }
func (s Reference) MarshalJSON() ([]byte, error)  { return marshalDomain(s) }
func (s *Reference) UnmarshalJSON(b []byte) error { return unmarshalDomain(b, s) }

func safeRefNumber(n, min float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= 9007199254740991 && math.Trunc(n) == n
}

type rawReference struct{ value any }

func eventRefRaw(v any) *rawReference {
	switch x := v.(type) {
	case Source:
		return x.eventRefRaw
	case Target:
		return x.eventRefRaw
	case FeedbackMessage:
		return x.eventRefRaw
	}
	return nil
}
func setEventRefRaw(v any, raw *rawReference) {
	switch x := v.(type) {
	case *Source:
		x.eventRefRaw = raw
	case *Target:
		x.eventRefRaw = raw
	case *FeedbackMessage:
		x.eventRefRaw = raw
	}
}
func eventRefValue(r *sourceevents.Ref, raw *rawReference) any {
	if raw != nil {
		return cloneReferenceValue(raw.value)
	}
	if r == nil {
		return nil
	}
	return object("eventId", r.EventID, "revision", float64(r.Revision), "deletionEpoch", float64(r.DeletionEpoch))
}
func cloneReferenceValue(v any) any {
	switch x := v.(type) {
	case *jsjson.Object:
		o := new(jsjson.Object)
		for _, k := range x.Keys() {
			_ = o.Set(k, cloneReferenceValue(getValue(x, k)))
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = cloneReferenceValue(v)
		}
		return a
	default:
		return v
	}
}
func referenceTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	default:
		return true
	}
}

func sameReference(a, b Reference) bool { return a.ID == b.ID && a.Version == b.Version }
