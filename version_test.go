package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeploymentIdentity(t *testing.T) {
	previous := buildRevision
	buildRevision = "0123456789abcdef"
	t.Cleanup(func() { buildRevision = previous })
	response := httptest.NewRecorder()
	withRecovery("version", handleVersion)(response, httptest.NewRequest(http.MethodGet, "/version", nil))
	var version map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version["revision"] != buildRevision || response.Header().Get("X-Release") != buildRevision {
		t.Fatalf("unexpected release identity: %s %v", response.Body.String(), response.Header())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("deployment identity must not be cached")
	}
}
