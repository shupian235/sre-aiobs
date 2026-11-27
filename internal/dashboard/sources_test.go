package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sre-aiobs/internal/config"
)

type checkTransport func(*http.Request) (*http.Response, error)

func (t checkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func TestSourcesUseOnlyConfiguredTargets(t *testing.T) {
	t.Setenv("TEST_SOURCE_TOKEN", "fixture-secret")
	calls := 0
	client := &http.Client{Transport: checkTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "elasticsearch.example.test" || r.Method != http.MethodPost || r.URL.Path != "/orders-*/_search" ||
			r.Header.Get("Authorization") != "ApiKey fixture-secret" {
			t.Error("unexpected probe request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"hits":{"total":{"value":14}}}`))}, nil
	})}
	settings := []config.Source{{Name: "orders", Kind: "elasticsearch", Environment: "test", URL: "https://elasticsearch.example.test",
		CredentialEnv: "TEST_SOURCE_TOKEN", Index: "orders-*", Fields: map[string]string{"level": "level", "timestamp": "@timestamp"}}}
	mux := http.NewServeMux()
	Mount(mux, &fixtureReader{}, "test", SourceAccess{Current: func() []config.Source { return settings }, HTTP: client})
	perform := func(method, path string, auth, header bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if auth {
			req.SetBasicAuth("operator", "test")
		}
		if header {
			req.Header.Set("X-SRE-AIObs-Check", "1")
		}
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	if r := perform("GET", "/api/sources", false, false); r.Code != 401 {
		t.Fatalf("unauthenticated list: %d", r.Code)
	}
	if r := perform("GET", "/api/sources", true, false); r.Code != 200 || !strings.Contains(r.Body.String(), `"backend":true`) || strings.Contains(r.Body.String(), "elasticsearch.example.test") || strings.Contains(r.Body.String(), "fixture-secret") {
		t.Fatalf("list response: %d %s", r.Code, r.Body)
	}
	if r := perform("POST", "/api/sources/unknown/check", true, true); r.Code != 404 {
		t.Fatalf("unknown check: %d", r.Code)
	}
	if r := perform("POST", "/api/sources/orders/check", true, false); r.Code != 400 {
		t.Fatalf("missing header: %d", r.Code)
	}
	if r := perform("POST", "/api/sources/orders/check?url=https://unused.example", true, true); r.Code != 200 || !strings.Contains(r.Body.String(), `"count":14`) {
		t.Fatalf("check response: %d %s", r.Code, r.Body)
	}
	if calls != 1 {
		t.Fatalf("expected only one upstream call, got %d", calls)
	}
}
