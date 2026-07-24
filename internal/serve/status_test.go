package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusIncludesRunningVersion(t *testing.T) {
	srv := &server{store: openAnalyticsStore(t), version: "v0.2.1"}
	if got := srv.buildStatus(context.Background()); got.Version != "v0.2.1" {
		t.Fatalf("status version = %q", got.Version)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	srv.handleStatus(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"version":"v0.2.1"`) {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}
