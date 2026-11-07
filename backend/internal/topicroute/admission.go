package topicroute

import (
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrAdmissionConfig   = errors.New("topic_admission_config_invalid")
	ErrAdmissionSnapshot = errors.New("topic_admission_snapshot_invalid")
	ErrTopicCue          = errors.New("topic_cue_invalid")
	// ErrAdmissionCollation is a migration blocker, not a source policy outcome.
	// No approximate or byte-order fallback is permissible for arbitrary topic IDs.
	ErrAdmissionCollation = errors.New("topic_admission_exact_locale_comparator_required")
)

type TopicSnapshot struct {
	ID, SceneID                           string
	TouchedAt                             float64
	Participants                          []string
	Cue                                   *Cue
	Provisional, Active, Pending, Waiting bool
	SendState                             string
}
type TopicAnchor struct{ Kind, TopicID string }
type TopicInput struct {
	SceneID, SenderID string
	Now               float64
	Anchors           []TopicAnchor
	UnavailableReply  bool
	Cue               *string
}

// LocaleCompare must implement the source Node runtime's default localeCompare,
// including equivalence (zero), ICU version, locale and unpaired surrogates.
// Nil is supported only when the result does not need a locale comparison.
// It is deliberately injected: x/text/collate is NOT equivalent to ICU78.3.
type LocaleCompare func(a, b string) (int, error)
type AdmissionConfig struct {
	MaxSlots, RecentMS *float64
	LocaleCompare      LocaleCompare
}
type TopicAssignment struct {
	Kind                  string
	TopicID, EvictTopicID *string
	Reason                string
	CandidateIDs          []string
	Cue                   *Cue
}

func jsSpace(u uint16) bool {
	return u == 0x9 || u == 0xb || u == 0xc || u == 0x20 || u == 0xa0 || u == 0xfeff || u == 0xa || u == 0xd || u == 0x2028 || u == 0x2029 || u == 0x1680 || (u >= 0x2000 && u <= 0x200a) || u == 0x202f || u == 0x205f || u == 0x3000
}
func trimUnits(u []uint16) []uint16 {
	for len(u) > 0 && jsSpace(u[0]) {
		u = u[1:]
	}
	for len(u) > 0 && jsSpace(u[len(u)-1]) {
		u = u[:len(u)-1]
	}
	return u
}

// Normalize valid scalar runs separately. Lone surrogates are normalization
// boundaries, retaining their precise canonical WTF-8 identity.
func normalizeCueUnits(u []uint16) string {
	var b strings.Builder
	start := 0
	for i := 0; i < len(u); i++ {
		if u[i] >= 0xd800 && u[i] <= 0xdbff && i+1 < len(u) && u[i+1] >= 0xdc00 && u[i+1] <= 0xdfff {
			i++
			continue
		}
		if u[i] >= 0xd800 && u[i] <= 0xdfff {
			b.WriteString(scalarNFC(jsjson.FromUTF16(u[start:i])))
			b.WriteString(jsjson.FromUTF16(u[i : i+1]))
			start = i + 1
		}
	}
	b.WriteString(scalarNFC(jsjson.FromUTF16(u[start:])))
	return b.String()
}

// ParseTopicCue ports the anchored Unicode regex, ECMAScript whitespace, trim
// and NFC. A nil label means no cue. Malformed non-WTF-8 is a boundary error.
func ParseTopicCue(text string) (*string, error) {
	u, err := jsjson.UTF16(text)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if strings.HasPrefix(text, "[topic:") {
		prefix = "[topic:"
	} else if strings.HasPrefix(text, "[话题:") {
		prefix = "[话题:"
	} else {
		return nil, nil
	}
	p, _ := jsjson.UTF16(prefix)
	i := len(p)
	for i < len(u) && jsSpace(u[i]) {
		i++
	}
	start := i
	count := 0
	for i < len(u) && u[i] != ']' && u[i] != '\r' && u[i] != '\n' && count < 32 {
		if u[i] >= 0xd800 && u[i] <= 0xdbff && i+1 < len(u) && u[i+1] >= 0xdc00 && u[i+1] <= 0xdfff {
			i++
		}
		i++
		count++
	}
	if count == 0 || i >= len(u) || u[i] != ']' {
		return nil, nil
	}
	label := normalizeCueUnits(trimUnits(u[start:i]))
	if label == "" {
		return nil, nil
	}
	return &label, nil
}
func utf16Less(a, b string) bool {
	// All strings are canonical WTF-8 at this package's input boundary.
	x, _ := jsjson.UTF16(a)
	y, _ := jsjson.UTF16(b)
	return slices.Compare(x, y) < 0
}
func candidateIDs(topics []TopicSnapshot) []string {
	ids := make([]string, 0, len(topics))
	for _, t := range topics {
		ids = append(ids, t.ID)
	}
	slices.SortFunc(ids, func(a, b string) int {
		if utf16Less(a, b) {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	return ids
}
func safeInteger(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n && math.Abs(n) <= 9007199254740991
}

// AssignTopic is pure policy: no source/ACK trust inference, models, UUIDs,
// mutation, scheduling permission or runtime effects. Input strings must be
// canonical WTF-8, as provided by jsjson. Caller owns snapshots during the call.
func AssignTopic(input TopicInput, topics []TopicSnapshot, config AdmissionConfig) (TopicAssignment, error) {
	maxSlots, recentMS := 4.0, 120000.0
	if config.MaxSlots != nil {
		maxSlots = *config.MaxSlots
	}
	if config.RecentMS != nil {
		recentMS = *config.RecentMS
	}
	if !safeInteger(maxSlots) || maxSlots < 1 || maxSlots > 16 || !safeInteger(recentMS) || recentMS < 1 || !safeInteger(input.Now) || input.Now < 0 {
		return TopicAssignment{}, ErrAdmissionConfig
	}
	scoped, ordinary := []TopicSnapshot{}, []TopicSnapshot{}
	seen := map[string]bool{}
	for _, t := range topics {
		if t.SceneID != input.SceneID {
			continue
		}
		if seen[t.ID] {
			return TopicAssignment{}, ErrAdmissionSnapshot
		}
		seen[t.ID] = true
		scoped = append(scoped, t)
		if t.TouchedAt <= input.Now && input.Now-t.TouchedAt <= recentMS && !t.Provisional {
			ordinary = append(ordinary, t)
		}
	}
	if float64(len(scoped)) > maxSlots {
		return TopicAssignment{}, ErrAdmissionSnapshot
	}
	allocate := func(provisional bool, reason string, ids []string, cue *Cue) (TopicAssignment, error) {
		if provisional {
			for _, t := range scoped {
				if t.Provisional {
					id := t.ID
					return TopicAssignment{Kind: "provisional", TopicID: &id, Reason: reason, CandidateIDs: ids}, nil
				}
			}
		}
		// Source sorts even below capacity, but that comparator has no effects. Only
		// demand its unavailable native implementation when it can change an output.
		out := TopicAssignment{Kind: "create", Reason: reason, CandidateIDs: ids, Cue: cue}
		if provisional {
			out.Kind = "provisional"
		}
		if float64(len(scoped)) < maxSlots {
			return out, nil
		}
		disposable := []TopicSnapshot{}
		for _, t := range scoped {
			if !t.Active && !t.Pending && !t.Waiting && (t.SendState == "none" || t.SendState == "settled") {
				disposable = append(disposable, t)
			}
		}
		if len(disposable) == 0 {
			return TopicAssignment{Kind: "deferred", Reason: "capacity_deferred", CandidateIDs: ids}, nil
		}
		// At the source's <=16 slots V8 TimSort uses one detected run and
		// binary insertion sort. Preserve that order even for nontransitive
		// comparisons involving NaN timestamps (not validated by the source).
		compare := func(a, b TopicSnapshot) (int, error) {
			d := a.TouchedAt - b.TouchedAt
			if d != 0 && !math.IsNaN(d) {
				if d < 0 {
					return -1, nil
				}
				return 1, nil
			}
			if a.ID == b.ID {
				return 0, nil
			}
			if config.LocaleCompare == nil {
				return 0, ErrAdmissionCollation
			}
			return config.LocaleCompare(a.ID, b.ID)
		}
		if err := sortAdmissionRun(disposable, compare); err != nil {
			return TopicAssignment{}, err
		}
		id := disposable[0].ID
		out.EvictTopicID = &id
		return out, nil
	}
	existing := func(t TopicSnapshot, reason string) (TopicAssignment, error) {
		id := t.ID
		return TopicAssignment{Kind: "existing", TopicID: &id, Reason: reason, CandidateIDs: []string{id}}, nil
	}
	empty := func() []string { return []string{} }
	for _, kind := range []string{"reply", "thread", "task"} {
		if kind != "reply" && input.UnavailableReply {
			return allocate(true, "reply_anchor_unavailable", empty(), nil)
		}
		ids := []string{}
		seen := map[string]bool{}
		for _, a := range input.Anchors {
			if a.Kind == kind && !seen[a.TopicID] {
				ids = append(ids, a.TopicID)
				seen[a.TopicID] = true
			}
		}
		if len(ids) == 0 {
			continue
		}
		slices.SortFunc(ids, func(a, b string) int {
			if utf16Less(a, b) {
				return -1
			}
			if a == b {
				return 0
			}
			return 1
		})
		if len(ids) != 1 {
			filtered := empty()
			for _, id := range ids {
				for _, t := range scoped {
					if t.ID == id {
						filtered = append(filtered, id)
						break
					}
				}
			}
			return allocate(true, "conflicting_anchors", filtered, nil)
		}
		for _, t := range scoped {
			if t.ID == ids[0] {
				return existing(t, kind+"_anchor")
			}
		}
		return allocate(true, "reply_anchor_unavailable", empty(), nil)
	}
	if input.UnavailableReply {
		return allocate(true, "reply_anchor_unavailable", empty(), nil)
	}
	if input.Cue != nil {
		u, err := jsjson.UTF16(*input.Cue)
		if err != nil {
			return TopicAssignment{}, err
		}
		label := normalizeCueUnits(trimUnits(u))
		n, _ := jsjson.CodePointCount(label)
		if label == "" || n > 32 || strings.ContainsAny(label, "\r\n[]") {
			return TopicAssignment{}, ErrTopicCue
		}
		cue := &Cue{input.SenderID, label}
		matches := []TopicSnapshot{}
		for _, t := range scoped {
			if t.Cue != nil && t.Cue.InitiatorID == cue.InitiatorID && t.Cue.Label == label {
				matches = append(matches, t)
			}
		}
		if len(matches) == 1 {
			return existing(matches[0], "explicit_cue")
		}
		if len(matches) > 1 {
			return allocate(true, "ambiguous", candidateIDs(matches), nil)
		}
		return allocate(false, "explicit_cue", empty(), cue)
	}
	participant := []TopicSnapshot{}
	for _, t := range ordinary {
		if slices.Contains(t.Participants, input.SenderID) {
			participant = append(participant, t)
		}
	}
	if len(participant) == 1 {
		return existing(participant[0], "participant_continuity")
	}
	if len(participant) > 1 {
		return allocate(true, "ambiguous", candidateIDs(participant), nil)
	}
	if len(ordinary) == 1 {
		return existing(ordinary[0], "single_conversation")
	}
	reason := "no_recent_topic"
	if len(ordinary) > 0 {
		reason = "ambiguous"
	}
	return allocate(true, reason, candidateIDs(ordinary), nil)
}

// Source V8's small-array stable sort, necessary when a snapshot contains NaN.
func sortAdmissionRun(a []TopicSnapshot, compare func(TopicSnapshot, TopicSnapshot) (int, error)) error {
	if len(a) < 2 {
		return nil
	}
	c, err := compare(a[1], a[0])
	if err != nil {
		return err
	}
	descending := c < 0
	run := 2
	for run < len(a) {
		c, err = compare(a[run], a[run-1])
		if err != nil {
			return err
		}
		if (descending && c >= 0) || (!descending && c < 0) {
			break
		}
		run++
	}
	if descending {
		slices.Reverse(a[:run])
	}
	for start := run; start < len(a); start++ {
		pivot := a[start]
		left, right := 0, start
		for left < right {
			mid := left + (right-left)/2
			c, err = compare(pivot, a[mid])
			if err != nil {
				return err
			}
			if c < 0 {
				right = mid
			} else {
				left = mid + 1
			}
		}
		copy(a[left+1:start+1], a[left:start])
		a[left] = pivot
	}
	return nil
}

// x/text normalizes stream-safe text by inserting CGJ after 30 nonstarters;
// ECMAScript does not. Decompose individual scalars, order their combining
// classes, then compose pairs with the canonical blocking rule instead.
func scalarNFC(s string) string {
	type point struct {
		r rune
		c uint8
	}
	points := make([]point, 0, len(s))
	for _, r := range s {
		for _, d := range norm.NFD.String(string(r)) {
			c := norm.NFD.PropertiesString(string(d)).CCC()
			points = append(points, point{d, c})
		}
	}
	// Stable counting buckets order each nonstarter run in linear time.
	// CCC is intrinsically one byte; 256 buckets are a Unicode property
	// width, not an admission cap. The source allows arbitrarily long cues.
	var scratch []point
	for start := 0; start < len(points); {
		if points[start].c == 0 {
			start++
			continue
		}
		end := start + 1
		for end < len(points) && points[end].c != 0 {
			end++
		}
		if end-start > 1 {
			var offsets [256]int
			for _, p := range points[start:end] {
				offsets[p.c]++
			}
			total := 0
			for c, n := range offsets {
				offsets[c] = total
				total += n
			}
			if cap(scratch) < end-start {
				scratch = make([]point, end-start)
			} else {
				scratch = scratch[:end-start]
			}
			for _, p := range points[start:end] {
				scratch[offsets[p.c]] = p
				offsets[p.c]++
			}
			copy(points[start:end], scratch)
		}
		start = end
	}
	out := make([]rune, 0, len(points))
	starter := -1
	var last uint8
	for _, p := range points {
		if starter >= 0 && (last == 0 || last < p.c) {
			pair := []rune(norm.NFC.String(string([]rune{out[starter], p.r})))
			if len(pair) == 1 {
				out[starter] = pair[0]
				continue
			}
		}
		if p.c == 0 {
			starter = len(out)
		}
		out = append(out, p.r)
		last = p.c
	}
	return string(out)
}
