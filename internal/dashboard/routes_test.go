package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sre-aiobs/internal/config"
)

type routeFixture struct {
	routes []config.Route
}

func (f *routeFixture) SourceRoutes(context.Context, string) ([]config.Route, bool, error) {
	return f.routes, f.routes != nil, nil
}

func (f *routeFixture) SaveSourceRoutes(_ context.Context, _ string, routes []config.Route) error {
	f.routes = routes
	return nil
}

func TestRouteEditorAuthorizationValidationAndReset(t *testing.T) {
	mux := http.NewServeMux()
	fixture := &routeFixture{}
	defaults := []config.Route{{Label: "/orders/:id", Pattern: "^/orders/[0-9]+$"}}
	mountRoutes(mux, func() []config.Source {
		return []config.Source{{Name: "nginx", Fields: map[string]string{"uri": "path"}, Routes: defaults}}
	}, fixture, func(handler http.Handler) http.HandlerFunc { return handler.ServeHTTP }, "admin")
	call := func(method, body, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/sources/nginx/routes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Alert-Settings-Password", password)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	if res := call("GET", "", ""); res.Code != 200 || !strings.Contains(res.Body.String(), defaults[0].Label) {
		t.Fatal("YAML fallback was not returned")
	}
	if res := call("PUT", "[]", ""); res.Code != 403 || fixture.routes != nil {
		t.Fatal("unauthorized write")
	}
	if res := call("PUT", `[{"label":"/bad","pattern":"["}]`, "admin"); res.Code != 400 || fixture.routes != nil {
		t.Fatal("invalid pattern accepted")
	}
	if res := call("PUT", "[]", "admin"); res.Code != 204 || fixture.routes == nil {
		t.Fatal("empty override was ignored")
	}
	if res := call("GET", "", ""); !strings.Contains(res.Body.String(), `"routes":[],"overridden":true`) {
		t.Fatalf("empty override did not take precedence: %s", res.Body.String())
	}
	if res := call("DELETE", "", "admin"); res.Code != 204 || fixture.routes != nil {
		t.Fatal("reset did not clear override")
	}
}
