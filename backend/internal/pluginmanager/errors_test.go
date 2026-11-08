package pluginmanager

import "testing"

func TestSafeFailureVocabulary(t *testing.T) {
	e := NewStartupError("secret https://private?token=oops", map[string]any{"code": "ECONNRESET", "httpStatus": float64(503), "message": "do not expose"})
	if e.Code != "plugin_setup_failed" || e.SystemCode != "ECONNRESET" || e.HTTPStatus != 503 {
		t.Fatal(e)
	}
	for _, v := range []map[string]any{
		{"systemCode": "private", "code": "ECONNRESET"},
		{"systemCode": false, "cause": map[string]any{"code": "ECONNRESET"}},
		{"httpStatus": float64(599.5)},
		{"httpStatus": float64(600)},
	} {
		if d := SafeNetworkDetails(v); d != (NetworkDetails{}) {
			t.Fatalf("unsafe details: %+v", d)
		}
	}
	if d := SafeNetworkDetails(map[string]any{"cause": map[string]any{"code": "EPIPE"}}); d.SystemCode != "EPIPE" {
		t.Fatal(d)
	}
	for code := range startupCodes {
		if NewStartupError(code, nil).Code != code {
			t.Fatal(code)
		}
	}
}
