package imagecodec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

func TestActualDisplaySharp(t *testing.T) {
	path := os.Getenv("HE_IMAGE_LIBRARY")
	if path == "" {
		path = os.Getenv("HUMANENGINE_VIPS_LIBRARY")
	}
	if path == "" {
		t.Skip("actual pinned native library required")
	}
	c, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("testdata/display-sharp.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Input string
			Mime, Error *string
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Cases) < 26 {
		t.Fatal("missing display cases")
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			b, e := base64.StdEncoding.DecodeString(test.Input)
			if e != nil {
				t.Fatal(e)
			}
			mime, e := c.ValidateDisplayImage(context.Background(), b)
			if test.Error != nil {
				if e == nil || e.Error() != *test.Error {
					t.Fatalf("got %s %v want %s", mime, e, *test.Error)
				}
			} else if e != nil || mime != *test.Mime {
				t.Fatalf("got %s %v want %s", mime, e, *test.Mime)
			}
		})
	}
}
