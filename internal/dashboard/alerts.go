package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"sre-aiobs/internal/platform"
)

type alertStore interface {
	AlertSettings(context.Context) (platform.AlertSettings, error)
	SaveAlertSettings(context.Context, platform.AlertSettings) error
}

func mountAlerts(mux *http.ServeMux, backend alertStore, protect func(http.Handler) http.HandlerFunc, adminPassword string, telegramDestination func() bool, encrypted bool) {
	expected := sha256.Sum256([]byte(adminPassword))
	const route = "/api/alert-settings"
	mux.HandleFunc("GET "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		settings, err := backend.AlertSettings(ctx)
		if err != nil {
			http.Error(w, "Alert settings unavailable", 503)
			return
		}
		telegram := telegramDestination != nil && telegramDestination()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			platform.AlertSettings
			SlackConfigured    bool `json:"slack_configured"`
			TelegramConfigured bool `json:"telegram_configured"`
			EmailConfigured    bool `json:"email_configured"`
			Editable           bool `json:"editable"`
			EmailEditable      bool `json:"email_editable"`
		}{settings, os.Getenv("SLACK_WEBHOOK_URL") != "", telegram && os.Getenv("TELEGRAM_BOT_TOKEN") != "", settings.EmailDestination.Configured(), adminPassword != "", encrypted && adminPassword != ""})
	})))
	mux.HandleFunc("PUT "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := sha256.Sum256([]byte(r.Header.Get("X-Alert-Settings-Password")))
		if adminPassword == "" || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			http.Error(w, "Alert settings authorization required", 403)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		var input struct {
			MinimumScore     int                        `json:"minimum_score"`
			CooldownMinutes  int                        `json:"cooldown_minutes"`
			Telegram         *bool                      `json:"telegram"`
			Slack            *bool                      `json:"slack"`
			Email            *bool                      `json:"email"`
			EmailDestination *platform.EmailDestination `json:"email_destination"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		var trailing any
		if decoder.Decode(&input) != nil || decoder.Decode(&trailing) != io.EOF || input.Telegram == nil || input.Slack == nil || input.Email == nil {
			http.Error(w, "Invalid alert settings", 400)
			return
		}
		if input.EmailDestination != nil && !encrypted {
			http.Error(w, "Email encryption is not configured", 403)
			return
		}
		settings := platform.AlertSettings{MinimumScore: input.MinimumScore, CooldownMinutes: input.CooldownMinutes, Telegram: *input.Telegram, Slack: *input.Slack, Email: *input.Email, EmailDestination: input.EmailDestination}
		if !settings.Valid() {
			http.Error(w, "Invalid alert settings", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		if err := backend.SaveAlertSettings(ctx, settings); err != nil {
			http.Error(w, "Unable to save alert settings", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
