package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sre-aiobs/internal/platform"
	"sre-aiobs/internal/store"
)

type fixtureReader struct {
	source, kind string
	since        time.Time
	minimum      int
}

func (f *fixtureReader) Dashboard(_ context.Context, source, kind string, since time.Time, minimum int) (store.Dashboard, error) {
	f.source, f.kind, f.since, f.minimum = source, kind, since, minimum
	empty := json.RawMessage("[]")
	return store.Dashboard{Runs: 2, Requests: 83, Trend: empty, Rules: empty, Events: empty, Tasks: empty}, nil
}

func (*fixtureReader) DashboardSources(context.Context) ([]string, error) {
	return []string{"edge"}, nil
}

func (f *fixtureReader) BackendLogs(_ context.Context, source, kind string, since time.Time, minimum int) (store.BackendLogs, error) {
	f.source, f.kind, f.since, f.minimum = source, kind, since, minimum
	return store.BackendLogs{Total: 3, Counts: map[string]int64{"backend_exception": 3}, Events: json.RawMessage("[]")}, nil
}

func (f *fixtureReader) Vulnerabilities(ctx context.Context, source, kind string, since time.Time, minimum int) (store.BackendLogs, error) {
	return f.BackendLogs(ctx, source, kind, since, minimum)
}

func (f *fixtureReader) NginxRiskRoutes(ctx context.Context, source, kind string, since time.Time, minimum int) (store.BackendLogs, error) {
	return f.BackendLogs(ctx, source, kind, since, minimum)
}

func TestDashboardRoutes(t *testing.T) {
	f := &fixtureReader{}
	mux := http.NewServeMux()
	Mount(mux, f, "test-password", SourceAccess{})
	request := func(path, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if password != "" {
			req.SetBasicAuth("operator", password)
		}
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	if r := request("/api/dashboard", ""); r.Code != 401 || r.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing authentication challenge")
	}
	if r := request("/api/dashboard?hours=2", "test-password"); r.Code != 400 {
		t.Fatal("invalid duration accepted")
	}
	if r := request("/api/dashboard?score=81", "test-password"); r.Code != 400 {
		t.Fatal("invalid score accepted")
	}
	if r := request("/", "test-password"); r.Code != 200 || !strings.Contains(r.Body.String(), "安全概览") {
		t.Fatalf("dashboard response: %d", r.Code)
	}
	if r := request("/app.js", "test-password"); r.Code != 200 || !strings.Contains(r.Body.String(), "async function refresh") {
		t.Fatalf("script response: %d", r.Code)
	}
	r := request("/api/dashboard?source=edge&hours=6&score=80", "test-password")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"requests":83`) || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response %d %s", r.Code, r.Body)
	}
	if f.source != "edge" || f.kind != "five_minutes" || f.minimum != 80 || time.Since(f.since).Round(time.Minute) != 6*time.Hour {
		t.Fatalf("incorrect filters %+v", f)
	}
	if r := request("/api/backend-logs", ""); r.Code != 401 {
		t.Fatalf("backend authentication = %d", r.Code)
	}
	if r := request("/api/backend-logs?score=82", "test-password"); r.Code != 400 {
		t.Fatalf("backend invalid filter = %d", r.Code)
	}
	if r := request("/api/backend-logs?source=orders&hours=1&score=80", "test-password"); r.Code != 200 || !strings.Contains(r.Body.String(), `"backend_exception":3`) {
		t.Fatalf("backend response = %d, %s", r.Code, r.Body)
	}
	if f.source != "orders" || f.kind != "five_minutes" || f.minimum != 80 {
		t.Fatalf("backend filters = %+v", f)
	}
	disabled := http.NewServeMux()
	Mount(disabled, f, "", SourceAccess{})
	res := httptest.NewRecorder()
	disabled.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != 503 {
		t.Fatalf("unconfigured dashboard response: %d", res.Code)
	}
}

type fixtureAlertStore struct {
	fixtureReader
	settings platform.AlertSettings
}

func (f *fixtureAlertStore) AlertSettings(context.Context) (platform.AlertSettings, error) {
	return f.settings, nil
}
func (f *fixtureAlertStore) SaveAlertSettings(_ context.Context, v platform.AlertSettings) error {
	f.settings = v
	return nil
}

func TestAlertSettingsAuthorizationAndValidation(t *testing.T) {
	t.Setenv("ALERT_SETTINGS_PASSWORD", "write-test")
	t.Setenv("SLACK_WEBHOOK_URL", "fixture-only-secret")
	f := &fixtureAlertStore{settings: platform.DefaultAlertSettings()}
	mux := http.NewServeMux()
	Mount(mux, f, "view-test", SourceAccess{})
	request := func(method, secret, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/alert-settings", strings.NewReader(body))
		req.SetBasicAuth("operator", "view-test")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Alert-Settings-Password", secret)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	settings := `{"minimum_score":93,"cooldown_minutes":30,"slack":true,"telegram":false,"email":false}`
	if r := request("PUT", "", settings); r.Code != 403 {
		t.Fatalf("unprivileged PUT = %d", r.Code)
	}
	if r := request("PUT", "write-test", `{"minimum_score":93,"cooldown_minutes":30}`); r.Code != 400 {
		t.Fatalf("incomplete PUT = %d", r.Code)
	}
	if r := request("PUT", "write-test", settings); r.Code != 204 {
		t.Fatalf("PUT = %d, %s", r.Code, r.Body)
	}
	r := request("GET", "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"minimum_score":93`) || !strings.Contains(r.Body.String(), `"slack_configured":true`) || strings.Contains(r.Body.String(), "fixture-only-secret") {
		t.Fatalf("GET = %d, %s", r.Code, r.Body)
	}
}
