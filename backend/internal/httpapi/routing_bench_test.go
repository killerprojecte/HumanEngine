package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkServeHTTPUnmigratedRoute(b *testing.B) {
	server := &Server{
		token:              fixtureToken,
		runtimeStatus:      &runtimeStatusOwner{},
		logStatus:          &logStatusOwner{},
		sceneConfiguration: &sceneConfigurationOwner{},
		configuration:      &configurationOwner{},
	}
	for _, path := range []string{"/api/unmigrated/plain", "/api/unmigrated/%E4%B8%AD%25%2F"} {
		b.Run(path, func(b *testing.B) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+fixtureToken)
			request.Header.Set("X-Request-ID", "benchmark")
			b.ReportAllocs()
			for b.Loop() {
				server.ServeHTTP(httptest.NewRecorder(), request)
			}
		})
	}
}
