//go:build humanengine_icu && cgo

package topicroute

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"os"
	"reflect"
	"testing"
)

func TestAdmissionWithPinnedNativeICU(t *testing.T) {
	raw, err := os.ReadFile("testdata/admission-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Locale string
		Cases  []struct{ Name, Raw, Result, Error string }
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	c, err := topiccollation.New(f.Locale)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, test := range f.Cases {
		t.Run(test.Name, func(t *testing.T) {
			input, topics, config := admissionDecode(t, test.Raw)
			config.LocaleCompare = c.Compare
			got, err := AssignTopic(input, topics, config)
			if test.Error != "" {
				if err == nil || err.Error() != test.Error {
					t.Fatalf("got %v want %s", err, test.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := admissionExpected(t, test.Result); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
}
