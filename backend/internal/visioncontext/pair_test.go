package visioncontext

import (
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestCandidatePairMatchesJSONIdentity(t *testing.T) {
	values := []string{
		"", "ordinary", "a,b", "a\u0000b", "\"a\\b\"", "a\nb",
		"\U0001f469\u200d\U0001f4bb", "\ufffd",
		jsjson.FromUTF16([]uint16{0xd800}),
		jsjson.FromUTF16([]uint16{0xd800, 0xdc00}),
		jsjson.FromUTF16([]uint16{0xdc00}),
		"\xff", "\xed\xa0\x80\xed\xb0\x80", // malformed bytes, noncanonical pair
	}
	keyByJSON, jsonByKey := map[string]candidatePair{}, map[candidatePair]string{}
	for _, message := range values {
		for _, asset := range values {
			key := pair(Candidate{Message: &Message{ID: message}, Part: Part{AssetID: asset}})
			want, err := jsjson.Stringify([]any{message, asset})
			if key.malformed != (err != nil) {
				t.Fatalf("unexpected malformed state for atoms %q, %q", message, asset)
			}
			if previous, ok := jsonByKey[key]; ok && want != previous {
				t.Fatalf("merged distinct JSON identities %q and %q", previous, want)
			}
			if previous, ok := keyByJSON[want]; ok && key != previous {
				t.Fatalf("split equivalent JSON identity %q", want)
			}
			keyByJSON[want], jsonByKey[key] = key, want
		}
	}
}
