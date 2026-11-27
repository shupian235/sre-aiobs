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

type alertStatusFixture struct{}

func (alertStatusFixture) AlertSettings(context.Context) (platform.AlertSettings, error) {
	return platform.DefaultAlertSettings(), nil
}

func (alertStatusFixture) SaveAlertSettings(context.Context, platform.AlertSettings) error {
	return nil
}

func TestAlertSettingsShowsConfiguredDestinationsWithoutSecrets(t *testing.T) {
	t.Setenv("SLACK_WEBHOOK_URL", "slack-fixture-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "telegram-fixture-secret")
	mux := http.NewServeMux()
	mountAlerts(mux, alertStatusFixture{}, func(h http.Handler) http.HandlerFunc { return h.ServeHTTP }, "test", func() (bool, bool) {
		return false, true
	})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/alert-settings", nil))
	var flags map[string]json.RawMessage
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &flags) != nil ||
		string(flags["slack_configured"]) != "true" || string(flags["telegram_configured"]) != "false" ||
		string(flags["email_configured"]) != "true" || strings.Contains(res.Body.String(), "fixture-secret") {
		t.Fatalf("unexpected destination configuration: %d %s", res.Code, res.Body.String())
	}
}
