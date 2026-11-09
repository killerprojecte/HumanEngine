package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Administration must be backed by the same actual Experience service and
// retained-source authority used by this installation, even before model tools
// are enabled. This test does not claim the remaining exposure gates exist.
func TestInstalledApplicationExperienceAdministration(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = nil
	app, err := Open(context.Background(), options)
	if err != nil {
		if app != nil {
			_ = app.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if app.Experience == nil || app.ExperienceHints == nil {
		t.Fatal("missing actual Experience owner")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/memory-experience?sceneId=synthetic-unregistered", nil)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 404 || !strings.Contains(response.Body.String(), "Scene unavailable") {
		t.Fatalf("actual Experience owner must reject absent scene: %d %s", response.Code, response.Body.String())
	}
}
