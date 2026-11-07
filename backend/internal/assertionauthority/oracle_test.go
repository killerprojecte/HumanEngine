//go:build humanengine_icu && cgo

package assertionauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

type oraclePorts struct {
	input *Object
	calls []string
}

func (p *oraclePorts) call(name string) error {
	p.calls = append(p.calls, name)
	if get(p.input, "fail") == name {
		return errors.New("synthetic_port_failure")
	}
	return nil
}
func (p *oraclePorts) CheckScope() error  { return nil }
func (p *oraclePorts) AssertReady() error { return p.call("ready") }
func (p *oraclePorts) Record(id string) (*Object, error) {
	e := p.call("record:" + id)
	return object(get(get(p.input, "records"), id)), e
}
func (p *oraclePorts) Proofs(id string) ([]any, error) {
	if e := p.call("proofs:" + id); e != nil {
		return nil, e
	}
	value := get(get(p.input, "proofs"), id)
	if value == nil {
		return []any{}, nil
	}
	return array(value)
}
func (p *oraclePorts) State(id string) (assertions.State, error) {
	if e := p.call("state:" + id); e != nil {
		return assertions.State{}, e
	}
	value := get(get(p.input, "states"), id)
	n := func(k string) int64 { v, _ := get(value, k).(float64); return int64(v) }
	return assertions.State{Version: n("version"), DeletionEpoch: n("deletionEpoch"), Deleted: get(value, "deleted") == true}, nil
}
func (p *oraclePorts) Revision(id string, version float64, key string) (*Object, error) {
	if e := p.call(fmt.Sprintf("revision:%s/%g/%s", id, version, key)); e != nil {
		return nil, e
	}
	value := object(get(get(get(p.input, "revisions"), id), fmt.Sprintf("%g", version)))
	if value == nil || scopeKey(get(get(value, "record"), "scope")) != key {
		return nil, nil
	}
	return value, nil
}
func (p *oraclePorts) Recalled(scene, message string) (bool, error) {
	if e := p.call("recalled:" + scene + "/" + message); e != nil {
		return false, e
	}
	list, _ := fieldArray(p.input, "recalled")
	for _, v := range list {
		if v == scene+"/"+message {
			return true, nil
		}
	}
	return false, nil
}
func (p *oraclePorts) Scene(id string) (*Object, error) {
	e := p.call("scene:" + id)
	return object(get(get(p.input, "scenes"), id)), e
}
func metadata(value any) *sourceevents.Metadata {
	if value == nil {
		return nil
	}
	i := get(value, "identity")
	r := get(value, "ref")
	revision, _ := get(r, "revision").(float64)
	epoch, _ := get(r, "deletionEpoch").(float64)
	return &sourceevents.Metadata{Identity: sourceevents.Identity{SceneID: str(get(i, "sceneId")), AdapterID: str(get(i, "adapterId")), AccountID: str(get(i, "accountId")), MessageID: str(get(i, "messageId")), SenderID: str(get(i, "senderId"))}, Ref: sourceevents.Ref{EventID: str(get(r, "eventId")), Revision: int64(revision), DeletionEpoch: int64(epoch)}}
}
func (p *oraclePorts) Registered(scene, message string) (*sourceevents.Metadata, error) {
	e := p.call("registered:" + scene + "/" + message)
	return metadata(get(get(p.input, "registered"), scene+"/"+message)), e
}
func (p *oraclePorts) HasEdits(i sourceevents.Identity) (bool, error) {
	e := p.call("edits:" + i.SceneID + "/" + i.MessageID)
	m := metadata(get(get(p.input, "registered"), i.SceneID+"/"+i.MessageID))
	return m != nil && m.Ref.Revision > 1, e
}
func (p *oraclePorts) Current(ref sourceevents.Ref, i sourceevents.Identity) (sourceevents.Eligibility, error) {
	e := p.call("current:" + i.SceneID + "/" + i.MessageID)
	m := metadata(get(get(p.input, "registered"), i.SceneID+"/"+i.MessageID))
	return sourceevents.Eligibility{Eligible: m != nil && m.Ref == ref}, e
}
func oracleContext(v any) Context {
	return Context{SceneID: str(get(v, "sceneId")), Simulation: get(v, "simulation") == true}
}
func oracleReference(v any) Reference {
	version, _ := get(v, "version").(float64)
	epoch, _ := get(v, "deletionEpoch").(float64)
	return Reference{ID: str(get(v, "id")), Version: version, DeletionEpoch: epoch}
}
func invoke(a *Authority, p *oraclePorts, op []any) (any, error) {
	method := str(op[0])
	args := op[1:]
	switch method {
	case "supported":
		return a.Supported(object(get(get(p.input, "records"), str(args[0]))))
	case "currentApplicable":
		return a.CurrentApplicable(object(get(get(p.input, "records"), str(args[0]))))
	case "sanitize":
		return a.Sanitize(object(get(get(p.input, "records"), str(args[0]))))
	case "historySupports":
		return a.HistorySupports(str(args[0]))
	case "historicalAssertion":
		r, e := a.HistoricalAssertion(oracleContext(args[0]), oracleReference(args[1]))
		return r.Value(), e
	case "relationAuthority":
		r, e := a.RelationAuthority(oracleContext(args[0]), oracleReference(args[1]))
		return r.Value(), e
	case "domainSourceCurrent":
		return a.DomainSourceCurrent(object(args[0]))
	case "parentBindingState":
		return ParentBindingState(object(args[0])), nil
	case "parentReason":
		r, e := EvaluateProofParents(object(args[0]), str(args[1]), ParentPorts{Current: func(id string) (*assertions.State, error) { s, e := p.State(id); return &s, e }, Revision: func(ref *Object) (bool, error) {
			v, e := p.Revision(str(get(ref, "id")), get(ref, "version").(float64), "scene:s")
			return v != nil, e
		}})
		o := new(Object)
		_ = o.Set("available", r.Available)
		_ = o.Set("reason", r.Reason)
		if r.ParentID != "" {
			_ = o.Set("parentId", r.ParentID)
		}
		return o, e
	}
	return nil, fmt.Errorf("unknown operation: %s", method)
}
func TestActualSourceAuthorityOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/authority.json")
	if e != nil {
		t.Fatal(e)
	}
	var envelope struct {
		Node, Locale, Timezone string
		Now                    int64
		Sources                map[string]string
		Cases                  []struct {
			Name  string
			Input json.RawMessage
			Steps []struct {
				Op, Result, Error string
				Calls             json.RawMessage
			}
		}
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		t.Fatal(e)
	}
	if envelope.Node != "v24.22.1" {
		t.Fatal(envelope.Node)
	}
	for name, hash := range envelope.Sources {
		bytes, e := os.ReadFile(filepath.Join("../../..", name))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != hash {
			t.Fatalf("regenerate source fixture: %s", name)
		}
	}
	parser, e := jsdate.New(envelope.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	collator, e := topiccollation.New(envelope.Locale)
	if e != nil {
		t.Fatal(e)
	}
	defer collator.Close()
	for _, c := range envelope.Cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, step := range c.Steps {
				input, e := jsjson.Parse(string(c.Input))
				if e != nil {
					t.Fatal(e)
				}
				op, e := jsjson.Parse(step.Op)
				if e != nil {
					t.Fatal(e)
				}
				ports := &oraclePorts{input: input.(*Object), calls: []string{}}
				a := &Authority{read: ports, events: ports, options: Options{DateParser: parser, Compare: collator.Compare, NowMillis: func() int64 { return envelope.Now }}}
				value, e := invoke(a, ports, op.([]any))
				actualError := ""
				if e != nil {
					actualError = e.Error()
					value = nil
				}
				if actualError != step.Error {
					t.Fatalf("%s error = %q; want %q", step.Op, actualError, step.Error)
				}
				if o, ok := value.(*Object); ok && o == nil {
					value = nil
				}
				actual, e := jsjson.Stringify(value)
				if e != nil {
					t.Fatal(e)
				}
				if actual != step.Result {
					t.Fatalf("%s\nactual %s\nwant   %s", step.Op, actual, step.Result)
				}
				expectedCalls, e := jsjson.Parse(string(step.Calls))
				if e != nil {
					t.Fatal(e)
				}
				actualCalls := make([]any, len(ports.calls))
				for i, call := range ports.calls {
					actualCalls[i] = call
				}
				if !reflect.DeepEqual(actualCalls, expectedCalls) {
					t.Fatalf("%s call order\nactual %v\nwant   %v", step.Op, actualCalls, expectedCalls)
				}
			}
		})
	}
}
