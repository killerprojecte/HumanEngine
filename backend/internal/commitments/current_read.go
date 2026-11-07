package commitments

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

// ProjectCommitment qualifies each original evidence occurrence independently.
// The callback is a current assertion authority, never historical storage.
func ProjectCommitment(ctx Context, head, revision *Object, current func(Context, Reference) (*Assertion, error)) (*Object, error) {
	evidence := []any{}
	groups := []any{}
	qualify := func(input []any) ([]any, error) {
		out := []any{}
		for _, raw := range input {
			e, err := CurrentEvidence(ctx, object(raw), current)
			if err != nil {
				return nil, err
			}
			if e != nil {
				out = append(out, e)
			}
		}
		return out, nil
	}
	var err error
	if revision != nil {
		evidence, err = qualify(arr(get(revision, "evidence")))
		if err != nil {
			return nil, err
		}
		for _, group := range arr(get(revision, "requiredProposalGroups")) {
			qualified, e := qualify(arr(group))
			if e != nil {
				return nil, e
			}
			groups = append(groups, qualified)
		}
	}
	proposed := false
	for _, e := range evidence {
		if get(e, "kind") == "proposal" {
			proposed = true
		}
	}
	for _, group := range groups {
		if len(arr(group)) == 0 {
			proposed = false
		}
	}
	participants := arr(get(get(revision, "terms"), "participantIds"))
	contains := func(values []any, value any) bool {
		for _, v := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	ids := func(kind string) []any {
		out := []any{}
		for _, e := range evidence {
			person := get(e, "personId")
			if get(e, "kind") == kind && str(person) != "" && contains(participants, person) && !contains(out, person) {
				out = append(out, person)
			}
		}
		return out
	}
	confirmed, complete, cancelled := ids("confirm"), ids("complete"), ids("cancel")
	full := revision != nil && proposed && canConfirm(revision)
	allComplete := true
	missing := []any{}
	for _, p := range participants {
		if !contains(confirmed, p) {
			full = false
			missing = append(missing, p)
		}
		if !contains(complete, p) {
			allComplete = false
		}
	}
	changed := get(head, "everActivatedVersion") != nil
	version := get(head, "activeVersion")
	if revision != nil {
		version = get(get(revision, "ref"), "version")
	}
	terminal := get(head, "everTerminal")
	hasTerminal := terminal != nil && get(terminal, "version") == version
	actualTerminal := ""
	if full && len(cancelled) > 0 {
		actualTerminal = "cancelled"
	} else if full && allComplete {
		actualTerminal = "done"
	}
	lostTerminal := hasTerminal && get(terminal, "state") != actualTerminal
	ref := get(revision, "ref")
	if revision == nil {
		version = get(head, "activeVersion")
		if version == nil {
			version = get(head, "latestVersion")
		}
		ref = obj("id", get(head, "id"), "version", version, "deletionEpoch", get(head, "deletionEpoch"))
	}
	out := obj("ref", clone(ref), "mutationVersion", get(head, "mutationVersion"), "sceneId", get(head, "sceneId"))
	if revision != nil && proposed {
		_ = out.Set("terms", clone(get(revision, "terms")))
		_ = out.Set("recordedAt", get(revision, "recordedAt"))
	}
	confirmation := "proposed"
	if lostTerminal {
		confirmation = "incomplete"
	} else if full {
		confirmation = "confirmed"
	} else if changed && (revision == nil || get(head, "activeVersion") == get(get(revision, "ref"), "version")) {
		confirmation = "incomplete"
	}
	lifecycle := actualTerminal
	if lifecycle == "" {
		lifecycle = "pending"
	}
	if lostTerminal {
		lifecycle = "needs_review"
	}
	for _, m := range obj("confirmation", confirmation, "lifecycle", lifecycle, "qualification", "declared_not_verified", "sourceBodyCoverage", "unavailable", "changed", changed, "confirmedParticipantIds", confirmed, "missingParticipantIds", missing, "completedParticipantIds", complete, "cancelledParticipantIds", cancelled, "evidence", evidence, "requiredProposalGroups", groups).Members() {
		_ = out.Set(m.Key, m.Value)
	}
	return out, nil
}

// ReadOptions preserves source list defaults while retaining invalid zero/NaN
// limits for validation, including when the service is disabled.
type ReadOptions struct {
	PersonID  string
	Limit     *float64
	Lifecycle *string
}

type ReadService struct {
	reader  *Reader
	current func(Context, Reference) (*Assertion, error)
	enabled func(Context) (bool, error)
	check   func() error
}

// newReadService binds the production authority to an already-owned assertion
// reader. Both readers must be borrowed from the same owner scope. No storage
// initialization, writer acquisition, or eligibility cache occurs here.
func newReadService(reader *Reader, assertionsReader *assertions.Reader, enabled func(Context) (bool, error), options assertionauthority.Options) (*ReadService, error) {
	if reader == nil {
		return nil, errors.New("commitment_reader_required")
	}
	if err := reader.CheckScope(); err != nil {
		return nil, err
	}
	if enabled == nil {
		return nil, errors.New("commitment_enabled_policy_required")
	}
	authority, err := assertionauthority.New(assertionsReader, options)
	if err != nil {
		return nil, err
	}
	return &ReadService{reader: reader, enabled: enabled, check: func() error {
		if err := reader.CheckScope(); err != nil {
			return err
		}
		return assertionsReader.CheckScope()
	}, current: func(ctx Context, ref Reference) (*Assertion, error) {
		return authority.RelationAuthority(assertionauthority.Context{SceneID: ctx.SceneID, Simulation: ctx.Simulation}, ref)
	}}, nil
}
func (s *ReadService) Current(ctx Context, ref Reference) (*Object, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if ctx.Simulation {
		return nil, nil
	}
	enabled, err := s.enabled(ctx)
	if err != nil || !enabled {
		return nil, err
	}
	head, err := s.reader.Head(ref.ID)
	if err != nil || head == nil {
		return nil, err
	}
	if get(head, "deleted") == true || get(head, "sceneId") != ctx.SceneID || num(get(head, "deletionEpoch")) != ref.DeletionEpoch || (get(head, "activeVersion") != ref.Version && get(head, "latestVersion") != ref.Version) {
		return nil, nil
	}
	revision, err := s.reader.Revision(ref.ID, ref.Version)
	if err != nil {
		return nil, err
	}
	return ProjectCommitment(ctx, head, revision, s.current)
}
func (s *ReadService) List(ctx Context, options ReadOptions) (*Object, error) {
	lifecycle := "all"
	if options.Lifecycle != nil {
		lifecycle = *options.Lifecycle
	}
	if lifecycle != "open" && lifecycle != "terminal" && lifecycle != "all" {
		return nil, errors.New("commitment_read_lifecycle")
	}
	limit := float64(12)
	if options.Limit != nil {
		limit = *options.Limit
	}
	if _, err := safe(1)(limit); err != nil || limit > 12 {
		return nil, errors.New("commitment_read_limit")
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	items := []any{}
	if ctx.Simulation {
		return obj("items", items, "coverage", "complete"), nil
	}
	enabled, err := s.enabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return obj("items", items, "coverage", "complete"), nil
	}
	heads, err := s.reader.Heads(ctx.SceneID)
	if err != nil {
		return nil, err
	}
	prefix := heads
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	for _, raw := range prefix {
		head := object(raw)
		if get(head, "deleted") == true {
			continue
		}
		first := get(head, "activeVersion")
		if first == nil {
			first = get(head, "latestVersion")
		}
		versions := []any{first}
		if first != get(head, "latestVersion") {
			versions = append(versions, get(head, "latestVersion"))
		}
		for _, version := range versions {
			revision, err := s.reader.Revision(str(get(head, "id")), num(version))
			if err != nil {
				return nil, err
			}
			item, err := ProjectCommitment(ctx, head, revision, s.current)
			if err != nil {
				return nil, err
			}
			// Source projects the complete prefix before filtering; do not short-circuit
			// projection on person/lifecycle or the requested output limit.
			items = append(items, item)
		}
	}
	filtered := []any{}
	for _, item := range items {
		if options.PersonID != "" {
			matched := false
			for _, p := range arr(get(get(item, "terms"), "participantIds")) {
				if p == options.PersonID {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}
		state := get(item, "lifecycle")
		if lifecycle == "open" && state != "pending" && state != "needs_review" {
			continue
		}
		if lifecycle == "terminal" && state != "done" && state != "cancelled" {
			continue
		}
		filtered = append(filtered, item)
	}
	coverage := "complete"
	if len(heads) > 200 || len(filtered) > int(limit) {
		coverage = "limited"
	}
	if len(filtered) > int(limit) {
		filtered = filtered[:int(limit)]
	}
	return obj("items", filtered, "coverage", coverage), nil
}

func canConfirm(revision *Object) bool {
	terms := get(revision, "terms")
	return get(terms, "polarity") == "positive" && get(terms, "modality") == "asserted" && len(arr(get(terms, "conditions"))) == 0 && get(terms, "temporalScope") != "past" && get(terms, "temporalScope") != "unspecified"
}
