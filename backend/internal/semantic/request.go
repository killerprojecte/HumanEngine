package semantic

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"time"
)

const RequestMaxBytes = 65536

// CompletionPort keeps the request testable. Production composition uses NativeModels,
// whose physical attempt boundary enforces WithModelReadGuard even with accounting off.
type CompletionPort interface {
	Complete(context.Context, providertransport.CompletionRequest) (*providertransport.Completion, error)
}

func proposalTool() *jsjson.Object {
	schema, _ := parseRequestObject(proposalSchemaJSON)
	return inputObject("name", "memory.semantic_propose", "description", "Propose a qualified semantic view; not a saved fact or destructive merge", "inputSchema", schema)
}
func RequestProposals(ctx context.Context, s *Input, router CompletionPort, guard func() error, timezone string, now time.Time) ([]*jsjson.Object, error) {
	if e := guard(); e != nil {
		return nil, e
	}
	projection := replyplan.NewContextProjection(s.SceneID())
	declarations := []any{}
	for i, v := range s.Assertions() {
		a := scObject(v)
		declarations = append(declarations, inputObject("ref", scGet(s.Selections()[i], "ref"), "record", scGet(a, "record"), "proofs", scGet(a, "proofs"), "rootCount", float64(len(scArr(scGet(a, "roots"))))))
	}
	mode := "same_subject_partition"
	if scGet(s.Value, "episodeOnly") == true {
		mode = "episode_only"
	}
	body := inputObject("promptVersion", PromptVersion, "now", now.UTC().Format("2006-01-02T15:04:05.000Z"), "timezone", timezone, "declarations", declarations, "totalDistinctSourceRoots", float64(len(scArr(scGet(s.Value, "roots")))), "coverage", "complete", "mode", mode)
	for _, k := range []string{"episodeAnchor", "anchorQualifications"} {
		if v, ok := s.Value.Get(k); ok {
			_ = body.Set(k, v)
		}
	}
	user, e := projection.Stringify(body)
	if e != nil {
		return nil, e
	}
	messages := []any{inputObject("role", "system", "content", semanticSystemPrompt), inputObject("role", "user", "content", user)}
	tool := proposalTool()
	wire, e := jsjson.Stringify(inputObject("messages", messages, "tools", []any{tool}))
	if e != nil {
		return nil, e
	}
	if len([]byte(wire)) > RequestMaxBytes {
		return nil, errors.New("semantic_input_byte_limit")
	}
	if e = guard(); e != nil {
		return nil, e
	}
	ctx = providertransport.WithModelReadGuard(ctx, func(context.Context) error { return guard() })
	schema, e := providerwire.DecodeObject([]byte(proposalSchemaJSON))
	if e != nil {
		return nil, e
	}
	response, e := router.Complete(ctx, providertransport.CompletionRequest{Role: "learning", Messages: []*providertransport.Message{{Visible: map[string]any{"role": "system", "content": semanticSystemPrompt}}, {Visible: map[string]any{"role": "user", "content": user}}}, Tools: []map[string]any{{"name": "memory.semantic_propose", "description": scGet(tool, "description"), "inputSchema": schema}}})
	if e != nil {
		return nil, e
	}
	if response == nil {
		return nil, errors.New("semantic_response_missing")
	}
	defer response.Release()
	if e = guard(); e != nil {
		return nil, e
	}
	if len(response.ToolCalls) > 3 {
		return nil, errors.New("semantic_proposal_limit")
	}
	out := []*jsjson.Object{}
	allowed := map[string]bool{}
	for _, v := range s.Selections() {
		allowed[semString(scObject(scGet(v, "ref")), "id")] = true
	}
	for _, raw := range response.ToolCalls {
		call, e := requestObject(raw)
		if e != nil {
			return nil, e
		}
		if scGet(call, "name") != "memory.semantic_propose" {
			return nil, errors.New("semantic_unexpected_tool")
		}
		args := scObject(scGet(call, "arguments"))
		if args == nil {
			return nil, ErrContract
		}
		parents, e := projection.RestoreArguments(inputObject("memoryIds", scGet(args, "selectedParentIds")))
		if e != nil {
			return nil, e
		}
		other := new(jsjson.Object)
		for _, m := range args.Members() {
			if m.Key != "selectedParentIds" {
				_ = other.Set(m.Key, m.Value)
			}
		}
		restored, e := projection.RestoreArguments(other)
		if e != nil {
			return nil, e
		}
		o := scObject(restored)
		_ = o.Set("selectedParentIds", scGet(parents, "memoryIds"))
		parsed, e := ValidateProposal(o)
		if e != nil {
			return nil, e
		}
		for _, id := range scArr(scGet(parsed, "selectedParentIds")) {
			if !allowed[id.(string)] {
				return nil, errors.New("semantic_parent_not_exposed")
			}
		}
		if scGet(s.Value, "episodeOnly") == true && scGet(parsed, "kind") != "episode_candidate" {
			return nil, errors.New("semantic_episode_only")
		}
		if anchor := scObject(scGet(parsed, "episodeAnchor")); anchor != nil {
			expected := scObject(scGet(s.Value, "episodeAnchor"))
			if expected == nil || scGet(anchor, "id") != scGet(expected, "id") || scGet(anchor, "version") != scGet(expected, "version") {
				return nil, errors.New("semantic_episode_anchor_unavailable")
			}
		}
		out = append(out, parsed)
	}
	if e = guard(); e != nil {
		return nil, e
	}
	return out, nil
}
func requestObject(v any) (*jsjson.Object, error) {
	if o, ok := v.(*jsjson.Object); ok {
		return o, nil
	}
	raw, e := providerwire.EncodeJSON(v)
	if e != nil {
		return nil, e
	}
	return parseRequestObject(string(raw))
}

// Zod source contract schema; verified byte-for-byte by the source oracle fixture.
const proposalSchemaJSON = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"kind":{"type":"string","enum":["summary_candidate","duplicate_candidate","episode_candidate"]},"selectedParentIds":{"minItems":2,"maxItems":12,"type":"array","items":{"type":"string","minLength":1,"maxLength":512}},"text":{"type":"string","minLength":1,"maxLength":6000},"episodeAnchor":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":512},"version":{"type":"integer","minimum":1,"maximum":9007199254740991}},"required":["id","version"],"additionalProperties":false},"hypothesis":{"type":"boolean"}},"required":["kind","selectedParentIds","text","hypothesis"],"additionalProperties":false}`

func parseRequestObject(raw string) (*jsjson.Object, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o := scObject(v)
	if o == nil {
		return nil, ErrContract
	}
	return o, nil
}
