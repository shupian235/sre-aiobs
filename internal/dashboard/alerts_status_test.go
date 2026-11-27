package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sre-aiobs/internal/platform"
)

type alertStatusFixture struct{ settings platform.AlertSettings }

func (f *alertStatusFixture) AlertSettings(context.Context) (platform.AlertSettings, error) {
	return f.settings, nil
}

func (f *alertStatusFixture) SaveAlertSettings(_ context.Context, settings platform.AlertSettings) error {
	f.settings = settings
	return nil
}

func TestAlertSettingsShowsConfiguredDestinationsWithoutSecrets(t *testing.T) {
	t.Setenv("SLACK_WEBHOOK_URL", "slack-fixture-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "telegram-fixture-secret")
	mux := http.NewServeMux()
	settings := platform.DefaultAlertSettings()
	settings.EmailDestination = &platform.EmailDestination{From: "sender@example.net", To: []string{"ops@example.net"}}
	mountAlerts(mux, &alertStatusFixture{settings: settings}, func(h http.Handler) http.HandlerFunc { return h.ServeHTTP }, "test", func() bool { return false }, true)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/alert-settings", nil))
	var flags map[string]json.RawMessage
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &flags) != nil ||
		string(flags["slack_configured"]) != "true" || string(flags["telegram_configured"]) != "false" ||
		string(flags["email_configured"]) != "true" || strings.Contains(res.Body.String(), "fixture-secret") {
		t.Fatalf("unexpected destination configuration: %d %s", res.Code, res.Body.String())
	}
}

func TestAlertSettingsValidatesAndSavesEmailDestination(t *testing.T) {
	mux := http.NewServeMux()
	store := &alertStatusFixture{settings: platform.DefaultAlertSettings()}
	mountAlerts(mux, store, func(h http.Handler) http.HandlerFunc { return h.ServeHTTP }, "test", nil, true)
	save := func(destination, password string) int {
		body := `{"minimum_score":85,"cooldown_minutes":15,"slack":true,"telegram":true,"email":true,"email_destination":` + destination + `}`
		req := httptest.NewRequest(http.MethodPut, "/api/alert-settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Alert-Settings-Password", password)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res.Code
	}
	valid := `{"from":"sender@example.net","to":["ops@example.net","duty@example.net"],"region":"ap-southeast-1"}`
	if got := save(valid, "wrong"); got != http.StatusForbidden {
		t.Fatalf("unauthorized update returned %d", got)
	}
	if got := save(`{"from":"not-an-email","to":["ops@example.net"]}`, "test"); got != http.StatusBadRequest {
		t.Fatalf("invalid email update returned %d", got)
	}
	if got := save(valid, "test"); got != http.StatusNoContent || !store.settings.EmailDestination.Configured() ||
		store.settings.EmailDestination.Region != "ap-southeast-1" || len(store.settings.EmailDestination.To) != 2 {
		t.Fatalf("valid update did not persist: %d %+v", got, store.settings.EmailDestination)
	}
	if got := save(`{"from":"","to":[],"region":""}`, "test"); got != http.StatusNoContent || store.settings.EmailDestination.Configured() {
		t.Fatalf("clear address update failed: %d %+v", got, store.settings.EmailDestination)
	}
}
