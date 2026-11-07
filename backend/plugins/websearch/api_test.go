package websearch

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestNodeProviderResultsAndURLs(t *testing.T) {
	data, err := os.ReadFile("testdata/node-api.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Provider, Body string
			Expected       []Result
		}
		URLs []struct{ URL, Expected string }
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		got, e := ParseAPI(c.Provider, c.Body)
		if e != nil || !reflect.DeepEqual(got, c.Expected) {
			t.Errorf("provider %s got %#v want %#v %v", c.Provider, got, c.Expected, e)
		}
	}
	for _, c := range f.URLs {
		if got := PublicURL(c.URL); got != c.Expected {
			t.Errorf("URL %s got %q want %q", c.URL, got, c.Expected)
		}
	}
}
