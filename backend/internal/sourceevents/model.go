// Package sourceevents implements metadata-only source revision authority.
// Strings use jsjson canonical WTF-8 until the explicit SQLite text boundary.
package sourceevents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"strconv"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const Coverage = "unavailable"
const maxSafe = int64(9007199254740991)

var ErrInvalid = errors.New("source_event_invalid")
var ErrCorrupt = errors.New("source_event_corrupt")

type Identity struct{ SceneID, AdapterID, AccountID, MessageID, SenderID string }
type Ref struct {
	EventID                 string
	Revision, DeletionEpoch int64
}
type Order struct {
	Kind         string
	Sequence     int64
	ExpectedBase Ref
}
type Change struct {
	Kind       string
	Identity   Identity
	BodyHash   string
	OccurredAt *string
	EditID     string
	Order      Order
}
type Metadata struct {
	Identity             Identity
	Ref                  Ref
	BodyHash, ObservedAt string
	OccurredAt           *string
}
type Head struct {
	Metadata  Metadata
	Sequence  *int64
	OrderMode *string
	Deleted   bool
}
type Mutation struct {
	Kind        string
	Identity    Identity
	Ref         Ref
	PreviousRef *Ref
	Metadata    *Metadata
	HeadHash    string
}
type Result struct {
	Status           string
	Ref, PreviousRef *Ref
}
type Eligibility struct {
	Eligible bool
	Reason   string
	Metadata *Metadata
}

func obj(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return float64(*p)
}
func (i Identity) Value() *jsjson.Object {
	return obj("sceneId", i.SceneID, "adapterId", i.AdapterID, "accountId", i.AccountID, "messageId", i.MessageID, "senderId", i.SenderID)
}
func (r Ref) Value() *jsjson.Object {
	return obj("eventId", r.EventID, "revision", float64(r.Revision), "deletionEpoch", float64(r.DeletionEpoch))
}
func (m Metadata) Value() *jsjson.Object {
	return obj("identity", m.Identity.Value(), "ref", m.Ref.Value(), "bodyHash", m.BodyHash, "observedAt", m.ObservedAt, "occurredAt", nullable(m.OccurredAt), "bodyCoverage", Coverage)
}
func (h Head) Value() *jsjson.Object {
	return obj("metadata", h.Metadata.Value(), "sequence", nullableInt(h.Sequence), "orderMode", nullable(h.OrderMode), "deleted", h.Deleted)
}
func (c Change) Value() *jsjson.Object {
	o := obj("kind", c.Kind, "identity", c.Identity.Value(), "bodyHash", c.BodyHash, "occurredAt", nullable(c.OccurredAt))
	if c.Kind == "edit" {
		_ = o.Set("editId", c.EditID)
		var order *jsjson.Object
		if c.Order.Kind == "platform_sequence" {
			order = obj("kind", c.Order.Kind, "sequence", float64(c.Order.Sequence))
		} else {
			order = obj("kind", c.Order.Kind, "expectedBase", c.Order.ExpectedBase.Value())
		}
		_ = o.Set("order", order)
	}
	return o
}
func (m Mutation) Value() *jsjson.Object {
	o := obj("kind", m.Kind, "identity", m.Identity.Value(), "ref", m.Ref.Value())
	if m.PreviousRef != nil {
		_ = o.Set("previousRef", m.PreviousRef.Value())
	}
	if m.Metadata != nil {
		_ = o.Set("metadata", m.Metadata.Value())
	}
	if m.HeadHash != "" {
		_ = o.Set("headHash", m.HeadHash)
	}
	return o
}
func (r Result) Value() *jsjson.Object {
	o := obj("status", r.Status)
	if r.Ref != nil {
		_ = o.Set("ref", r.Ref.Value())
	}
	if r.PreviousRef != nil {
		_ = o.Set("previousRef", r.PreviousRef.Value())
	}
	_ = o.Set("bodyCoverage", Coverage)
	return o
}
func (e Eligibility) Value() *jsjson.Object {
	o := obj("eligible", e.Eligible, "reason", e.Reason)
	if e.Metadata != nil {
		_ = o.Set("metadata", e.Metadata.Value())
	}
	_ = o.Set("bodyCoverage", Coverage)
	return o
}
func digest(v any) (string, error) {
	b, e := jsjson.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func IdentityKey(i Identity) (string, error) {
	if !i.Valid() {
		return "", ErrInvalid
	}
	return digest([]any{i.SceneID, i.AdapterID, i.AccountID, i.MessageID, i.SenderID})
}
func HeadHash(m Metadata, sequence *int64, mode *string) (string, error) {
	key, e := IdentityKey(m.Identity)
	if e != nil {
		return "", e
	}
	return digest([]any{key, m.Ref.EventID, float64(m.Ref.Revision), float64(m.Ref.DeletionEpoch), m.BodyHash, m.ObservedAt, nullable(m.OccurredAt), Coverage, nullableInt(sequence), nullable(mode)})
}
func ChangeHash(c Change) (string, error) {
	if !c.Valid() {
		return "", ErrInvalid
	}
	return digest(c.Value())
}
func identifier(s string) bool {
	n, e := jsjson.CodePointCount(s)
	return e == nil && n >= 1 && n <= 512
}
func (i Identity) Valid() bool {
	return identifier(i.SceneID) && identifier(i.AdapterID) && identifier(i.AccountID) && identifier(i.MessageID) && identifier(i.SenderID)
}
func (r Ref) Valid() bool {
	return identifier(r.EventID) && r.Revision > 0 && r.Revision <= maxSafe && r.DeletionEpoch >= 0 && r.DeletionEpoch <= maxSafe
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func instant(s string) bool {
	if !instantPattern.MatchString(s) {
		return false
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || d < 1 {
		return false
	}
	days := [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days[1] = 29
	}
	return d <= days[m-1]
}
func (c Change) Valid() bool {
	if !c.Identity.Valid() || !hashPattern.MatchString(c.BodyHash) || (c.OccurredAt != nil && !instant(*c.OccurredAt)) {
		return false
	}
	if c.Kind == "ingress" {
		return c.EditID == "" && c.Order == (Order{})
	}
	return c.Kind == "edit" && identifier(c.EditID) && ((c.Order.Kind == "platform_sequence" && c.Order.Sequence >= 0 && c.Order.Sequence <= maxSafe && c.Order.ExpectedBase == (Ref{})) || (c.Order.Kind == "observed_cas" && c.Order.ExpectedBase.Valid() && c.Order.Sequence == 0))
}
func (m Metadata) Valid() bool {
	return m.Identity.Valid() && m.Ref.Valid() && hashPattern.MatchString(m.BodyHash) && instant(m.ObservedAt) && (m.OccurredAt == nil || instant(*m.OccurredAt))
}
func (h Head) Valid() bool {
	if !h.Metadata.Valid() {
		return false
	}
	if h.Metadata.Ref.Revision == 1 {
		return h.OrderMode == nil && h.Sequence == nil
	}
	if h.OrderMode == nil {
		return false
	}
	return (*h.OrderMode == "observed_cas" && h.Sequence == nil) || (*h.OrderMode == "platform_sequence" && h.Sequence != nil && *h.Sequence >= 0 && *h.Sequence <= maxSafe)
}
func object(v any, keys ...string) (*jsjson.Object, bool) {
	o, ok := v.(*jsjson.Object)
	if !ok || o.Len() != len(keys) {
		return nil, false
	}
	for _, k := range keys {
		if _, ok = o.Get(k); !ok {
			return nil, false
		}
	}
	return o, true
}
func get(o *jsjson.Object, k string) any    { v, _ := o.Get(k); return v }
func str(o *jsjson.Object, k string) string { s, _ := get(o, k).(string); return s }
func number(v any) (int64, bool) {
	n, ok := v.(float64)
	return int64(n), ok && !math.IsNaN(n) && n >= 0 && n <= float64(maxSafe) && math.Trunc(n) == n
}
func optionalString(v any) (*string, bool) {
	if v == nil {
		return nil, true
	}
	s, ok := v.(string)
	return &s, ok
}
func ParseIdentity(v any) (Identity, error) {
	o, ok := object(v, "sceneId", "adapterId", "accountId", "messageId", "senderId")
	if !ok {
		return Identity{}, ErrInvalid
	}
	i := Identity{str(o, "sceneId"), str(o, "adapterId"), str(o, "accountId"), str(o, "messageId"), str(o, "senderId")}
	if !i.Valid() {
		return Identity{}, ErrInvalid
	}
	return i, nil
}
func ParseRef(v any) (Ref, error) {
	o, ok := object(v, "eventId", "revision", "deletionEpoch")
	if !ok {
		return Ref{}, ErrInvalid
	}
	r, a := number(get(o, "revision"))
	d, b := number(get(o, "deletionEpoch"))
	ref := Ref{str(o, "eventId"), r, d}
	if !a || !b || !ref.Valid() {
		return Ref{}, ErrInvalid
	}
	return ref, nil
}
func ParseChange(v any) (Change, error) {
	o, ok := v.(*jsjson.Object)
	if !ok {
		return Change{}, ErrInvalid
	}
	kind := str(o, "kind")
	keys := []string{"kind", "identity", "bodyHash", "occurredAt"}
	if kind == "edit" {
		keys = append(keys, "editId", "order")
	}
	if _, ok = object(v, keys...); !ok {
		return Change{}, ErrInvalid
	}
	i, e := ParseIdentity(get(o, "identity"))
	at, ok := optionalString(get(o, "occurredAt"))
	if e != nil || !ok {
		return Change{}, ErrInvalid
	}
	c := Change{Kind: kind, Identity: i, BodyHash: str(o, "bodyHash"), OccurredAt: at}
	if kind == "edit" {
		c.EditID = str(o, "editId")
		order, ok := get(o, "order").(*jsjson.Object)
		if !ok {
			return Change{}, ErrInvalid
		}
		c.Order.Kind = str(order, "kind")
		if c.Order.Kind == "platform_sequence" {
			if _, ok = object(order, "kind", "sequence"); !ok {
				return Change{}, ErrInvalid
			}
			c.Order.Sequence, ok = number(get(order, "sequence"))
			if !ok {
				return Change{}, ErrInvalid
			}
		} else {
			if _, ok = object(order, "kind", "expectedBase"); !ok {
				return Change{}, ErrInvalid
			}
			c.Order.ExpectedBase, e = ParseRef(get(order, "expectedBase"))
			if e != nil {
				return Change{}, e
			}
		}
	}
	if !c.Valid() {
		return Change{}, ErrInvalid
	}
	return c, nil
}
func ParseMetadata(v any) (Metadata, error) {
	o, ok := object(v, "identity", "ref", "bodyHash", "observedAt", "occurredAt", "bodyCoverage")
	if !ok {
		return Metadata{}, ErrCorrupt
	}
	i, e := ParseIdentity(get(o, "identity"))
	r, f := ParseRef(get(o, "ref"))
	at, ok := optionalString(get(o, "occurredAt"))
	m := Metadata{i, r, str(o, "bodyHash"), str(o, "observedAt"), at}
	if e != nil || f != nil || !ok || str(o, "bodyCoverage") != Coverage || !m.Valid() {
		return Metadata{}, ErrCorrupt
	}
	return m, nil
}
func ParseHead(v any) (Head, error) {
	o, ok := object(v, "metadata", "sequence", "orderMode", "deleted")
	if !ok {
		return Head{}, ErrCorrupt
	}
	m, e := ParseMetadata(get(o, "metadata"))
	mode, a := optionalString(get(o, "orderMode"))
	deleted, b := get(o, "deleted").(bool)
	h := Head{Metadata: m, OrderMode: mode, Deleted: deleted}
	if v := get(o, "sequence"); v != nil {
		n, ok := number(v)
		if !ok {
			return Head{}, ErrCorrupt
		}
		h.Sequence = &n
	}
	if e != nil || !a || !b || !h.Valid() {
		return Head{}, ErrCorrupt
	}
	return h, nil
}
