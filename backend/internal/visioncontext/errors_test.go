package visioncontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestActualSourceOwnerErrorBoundaries(t *testing.T) {
	data, e := os.ReadFile("testdata/errors.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Port        string
			At          int
			ReadFailure bool
			Counts      map[string]int
			Error       string
			Result      json.RawMessage
		}
	}
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixture.Cases {
		t.Run(f.Port+string(rune('0'+f.At))+map[bool]string{true: "catch", false: "normal"}[f.ReadFailure], func(t *testing.T) {
			counts := map[string]int{"source": 0, "recalled": 0, "lookup": 0, "canRecognize": 0, "read": 0}
			ownerError := errors.New("synthetic_owner_failure")
			call := func(name string) error {
				counts[name]++
				if name == f.Port && counts[name] == f.At {
					return ownerError
				}
				return nil
			}
			p := Ports{Recalled: func(string, string) (bool, error) { return false, call("recalled") }, SourceAvailable: func(Candidate) (bool, error) { return true, call("source") }, ReadImage: func(context.Context, Candidate) ([]byte, error) {
				counts["read"]++
				if f.ReadFailure {
					return nil, errors.New("public_http_403")
				}
				return []byte("synthetic"), nil
			}, ImageDataURL: func(context.Context, []byte) (string, error) { t.Fatal("unexpected codec"); return "", nil }, Complete: func(context.Context, []ChatMessage) (string, error) { t.Fatal("unexpected provider"); return "", nil }, AddDependency: func(string) {}, AddSourceValidation: func(Candidate) {}}
			library := &Library{Lookup: func(Candidate) (string, bool, error) { return "", false, call("lookup") }, CanRecognize: func(Candidate) (bool, error) { return true, call("canRecognize") }, Recognize: func(context.Context, []byte, Candidate) (string, bool, error) { return "", true, nil }}
			result, e := Describe(context.Background(), &Turn{}, Config{SceneID: "s", Mode: "real"}, []*Message{{ID: "m", SceneID: "s", Status: "available", SentAt: "2026-01-01T00:00:00.000Z", Parts: []Part{{Type: "media", MediaType: "image", AssetID: "a"}}}}, &Cache{}, p, library)
			if !reflect.DeepEqual(counts, f.Counts) {
				t.Fatalf("counts %v source %v", counts, f.Counts)
			}
			if f.Error != "" {
				if e != ownerError || e.Error() != f.Error {
					t.Fatalf("error %v source %s", e, f.Error)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			actual, e := json.Marshal(result)
			if e != nil {
				t.Fatal(e)
			}
			var a, b any
			if e = json.Unmarshal(actual, &a); e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(f.Result, &b); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("result %s source %s", actual, f.Result)
			}
		})
	}
}
