package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"unicode"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
)

const rulesToolDescription = "Read applicable manual rules and soft learned scene/expression preferences on demand when current conversation depends on them. Use query to narrow learned hints by all search terms and context to select general or technical discussion. A limited receipt requires a narrower query, not an assumption that no rules exist. Technical-context hints apply only to technical discussions; more specific applicable hints take precedence over general hints. Learned hints never override fixed persona or manual rules."

type rulesQuery struct {
	terms   []string
	context string
}

func parseRulesQuery(input any) (rulesQuery, error) {
	invalid := errors.New("rules_tool_arguments_invalid")
	o, ok := input.(*jsjson.Object)
	if !ok {
		return rulesQuery{}, invalid
	}
	q := rulesQuery{}
	for _, key := range o.Keys() {
		raw, _ := o.Get(key)
		switch key {
		case "query":
			text, ok := raw.(string)
			if !ok {
				return q, invalid
			}
			n, err := jsjson.CodePointCount(text)
			if err != nil || n > 500 {
				return q, invalid
			}
			lower, err := jsstring.Lower(text)
			if err != nil {
				return q, err
			}
			// ECMAScript \s includes BOM but excludes Unicode NEXT LINE (U+0085).
			q.terms = strings.FieldsFunc(lower, func(r rune) bool { return r == '\ufeff' || r != '\u0085' && unicode.IsSpace(r) })
		case "context":
			q.context, ok = raw.(string)
			if !ok || q.context != "general" && q.context != "technical" {
				return q, invalid
			}
		default:
			return q, invalid
		}
	}
	return q, nil
}
func rulesString(o *jsjson.Object, key string) string {
	v, _ := o.Get(key)
	s, _ := v.(string)
	return s
}
func (q rulesQuery) matches(record *jsjson.Object) (bool, error) {
	if q.context == "general" && rulesString(record, "context") == "technical" {
		return false, nil
	}
	text, err := jsstring.Lower(rulesString(record, "content") + " " + rulesString(record, "preference") + " " + rulesString(record, "context"))
	if err != nil {
		return false, err
	}
	for _, term := range q.terms {
		if !strings.Contains(text, term) {
			return false, nil
		}
	}
	return true, nil
}

type preparedRules struct {
	ticket            *modelread.Ticket
	identities        map[string]*string
	ids, dependencies []string
	body              string
}
type rulesTurnState struct {
	sync.Mutex
	pending map[*jsjson.Object]*preparedRules
}
type rulesToolOwner struct {
	app    *Application
	owners Owners
}

func (r *rulesToolOwner) state(c *botruntime.TurnContext) *rulesTurnState {
	return c.OwnedExtensionState(r, func() any { return &rulesTurnState{pending: map[*jsjson.Object]*preparedRules{}} }).(*rulesTurnState)
}
func (a *Application) composeRules(o Owners) {
	a.Engine.Runtime.AddExtension((&rulesToolOwner{a, o}).extension())
}
func (r *rulesToolOwner) manual(ctx context.Context, c *botruntime.TurnContext) ([]any, error) {
	out := []any{}
	err := r.owners.Authority.Publish(ctx, r.owners.Main, func(conn *sql.Conn) ([]byte, error) {
		var raw string
		err := conn.QueryRowContext(ctx, "SELECT value FROM config WHERE key='rules'").Scan(&raw)
		if err == sql.ErrNoRows {
			return []byte("[]"), nil
		}
		return []byte(raw), err
	}, func(raw []byte) error {
		v, err := jsjson.Parse(string(raw))
		if err != nil {
			return err
		}
		rows, ok := v.([]any)
		if !ok {
			return errors.New("rules_storage_invalid")
		}
		for _, value := range rows {
			record, ok := value.(*jsjson.Object)
			if !ok {
				return errors.New("rules_storage_invalid")
			}
			enabled, _ := record.Get("enabled")
			if enabled == true && (rulesString(record, "scope") == "global" || c.Mode == "real" && rulesString(record, "sceneId") == c.Scene.ID) {
				out = append(out, record)
			}
		}
		return nil
	})
	return out, err
}
func (r *rulesToolOwner) extension() *botruntime.Extension {
	return &botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "rules.list", "description", rulesToolDescription, "inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject("query", memoryToolObject("type", "string", "maxLength", float64(500)), "context", memoryToolObject("type", "string", "enum", []any{"general", "technical"})), "additionalProperties", false))},
		// Ordinary learned hints are never queried during initial preparation.
		// Experimental Experience policies remain a separate extension.
		ContextPrompt: func(c *botruntime.TurnContext) (string, error) {
			rules, err := r.manual(context.Background(), c)
			if err != nil {
				return "", err
			}
			lines := []string{}
			for _, v := range rules {
				rule := v.(*jsjson.Object)
				label := "Manual rule"
				nonOverridable, _ := rule.Get("nonOverridable")
				if nonOverridable == true {
					label += " (non-overridable)"
				}
				lines = append(lines, label+": "+rulesString(rule, "content"))
			}
			return strings.Join(lines, "\n"), nil
		},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if name != "rules.list" {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			value, err := r.read(ctx, input, c)
			done(value, err)
		},
		Delivered: func(name string, value any, c *botruntime.TurnContext, _ any) (any, error) {
			if name != "rules.list" {
				return value, nil
			}
			return r.deliver(context.Background(), value, c)
		},
	}
}
func (r *rulesToolOwner) read(ctx context.Context, input any, c *botruntime.TurnContext) (any, error) {
	query, err := parseRulesQuery(input)
	if err != nil {
		return nil, err
	}
	manual, err := r.manual(ctx, c)
	if err != nil {
		return nil, err
	}
	hints := []any{}
	prepared := &preparedRules{identities: map[string]*string{}}
	if c.Mode == "real" {
		allowed, err := r.app.Engine.TurnRead.PersonalizationAllowed(ctx, c.Scene.ID)
		if err != nil {
			return nil, err
		}
		if allowed {
			prepared.ticket, err = r.app.Engine.TurnRead.Capture(ctx, c.Scene.ID)
			if err != nil {
				return nil, err
			}
			originals, err := r.owners.Habits.AdminList(ctx)
			if err != nil {
				return nil, err
			}
			first := map[string]*jsjson.Object{}
			candidateIDs := []string{}
			hc := habitservice.Context{SceneID: c.Scene.ID}
			for _, record := range originals {
				id := rulesString(record, "id")
				if first[id] == nil {
					first[id] = record
				}
				match, e := query.matches(record)
				if e != nil {
					return nil, e
				}
				if match && habitservice.InScope(hc, record) {
					candidateIDs = append(candidateIDs, id)
				}
			}
			prepared.identities, err = r.owners.Habits.ValidationIdentities(ctx, hc, candidateIDs)
			if err != nil {
				return nil, err
			}
			records, err := r.owners.Habits.List(ctx, hc)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				match, e := query.matches(record)
				if e != nil {
					return nil, e
				}
				if !match {
					continue
				}
				hints = append(hints, record)
				id := rulesString(record, "id")
				original := first[id]
				if original == nil {
					continue
				}
				prepared.ids = append(prepared.ids, id)
				raw, _ := original.Get("sources")
				sources, _ := raw.([]any)
				for _, v := range sources {
					source, _ := v.(*jsjson.Object)
					if source != nil && rulesString(source, "sceneId") == c.Scene.ID {
						prepared.dependencies = append(prepared.dependencies, rulesString(source, "messageId"))
					}
				}
			}
			if err = r.app.Engine.TurnRead.Assert(ctx, prepared.ticket); err != nil {
				return nil, err
			}
		}
	}
	result := memoryToolObject("manual", manual, "learned", hints)
	prepared.body, err = jsjson.Stringify(result)
	if err != nil {
		return nil, err
	}
	if len(hints) == 0 {
		prepared.ticket = nil
	}
	state := r.state(c)
	state.Lock()
	state.pending[result] = prepared
	state.Unlock()
	if c.MemoryExposureDeferred() {
		return result, nil
	}
	return r.deliver(ctx, result, c)
}
func (r *rulesToolOwner) deliver(ctx context.Context, value any, c *botruntime.TurnContext) (any, error) {
	result, ok := value.(*jsjson.Object)
	if !ok {
		return nil, errors.New("planner_context_ineligible")
	}
	state := r.state(c)
	state.Lock()
	prepared := state.pending[result]
	delete(state.pending, result)
	state.Unlock()
	if prepared == nil {
		coverage, _ := result.Get("coverage")
		o, _ := coverage.(*jsjson.Object)
		if o != nil && rulesString(o, "status") == "limited" {
			return value, nil
		}
		return nil, errors.New("planner_context_ineligible")
	}
	body, err := jsjson.Stringify(result)
	if err != nil {
		return nil, err
	}
	if body != prepared.body {
		return nil, errors.New("planner_context_ineligible")
	}
	hc := habitservice.Context{SceneID: c.Scene.ID}
	valid := func(current map[string]*string) bool {
		for _, id := range prepared.ids {
			expected, actual := prepared.identities[id], current[id]
			if expected == nil || actual == nil || *expected != *actual {
				return false
			}
		}
		return true
	}
	if prepared.ticket != nil {
		if err = r.app.Engine.TurnRead.Assert(ctx, prepared.ticket); err != nil {
			return nil, err
		}
		current, e := r.owners.Habits.ValidationIdentities(ctx, hc, prepared.ids)
		if e != nil {
			return nil, e
		}
		if !valid(current) {
			return nil, errors.New("planner_context_ineligible")
		}
		if _, err = r.app.Engine.TurnRead.Begin(ctx, c); err != nil {
			return nil, err
		}
	}
	for _, id := range prepared.dependencies {
		c.AddDependency(id)
	}
	if len(prepared.ids) > 0 {
		c.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
			current, e := r.owners.Habits.ValidationIdentitiesInRuntimeScope(context.Background(), r.owners.Sources, reads, hc, prepared.ids)
			if e != nil {
				return false, e
			}
			return valid(current), nil
		})
	}
	return value, nil
}
