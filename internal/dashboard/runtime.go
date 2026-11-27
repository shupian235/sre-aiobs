package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/store"
)

type runtimeStore interface {
	RuntimeSettings(context.Context) (config.RuntimeSettings, int64, error)
	SaveRuntimeSettings(context.Context, config.RuntimeSettings, int64) error
}

func mountRuntime(mux *http.ServeMux, backend runtimeStore, access SourceAccess, protect func(http.Handler) http.HandlerFunc, password string) {
	const route = "/api/runtime-settings"
	expected := sha256.Sum256([]byte(password))
	mux.HandleFunc("GET "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var settings config.RuntimeSettings
		var version int64
		if access.SettingsEncrypted {
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			var err error
			settings, version, err = backend.RuntimeSettings(ctx)
			if err != nil {
				http.Error(w, "Saved settings unavailable", 503)
				return
			}
		}
		credentialsConfigured := settings.Credentials != nil
		settings.Credentials = nil
		if settings.AWS == nil {
			settings.AWS = []config.ManagedAWS{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			config.RuntimeSettings
			Version               int64 `json:"version"`
			CredentialsConfigured bool  `json:"credentials_configured"`
			Editable              bool  `json:"editable"`
		}{settings, version, credentialsConfigured, access.SettingsEncrypted && password != ""})
	})))
	mux.HandleFunc("PUT "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := sha256.Sum256([]byte(r.Header.Get("X-Alert-Settings-Password")))
		if !access.SettingsEncrypted || password == "" || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			http.Error(w, "Settings authorization or encryption required", 403)
			return
		}
		mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaErr != nil || mediaType != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		var input struct {
			config.RuntimeSettings
			Version          int64 `json:"version"`
			ClearCredentials bool  `json:"clear_credentials"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF || input.Version < 0 ||
			(input.ClearCredentials && input.Credentials != nil) {
			http.Error(w, "Invalid settings body", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		if !input.ClearCredentials && input.Credentials == nil {
			previous, version, err := backend.RuntimeSettings(ctx)
			if err != nil {
				http.Error(w, "Saved settings unavailable", 503)
				return
			}
			if version != input.Version {
				http.Error(w, "Settings changed; reload the page", 409)
				return
			}
			input.Credentials = previous.Credentials
		}
		if err := input.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if access.BaseConfig != nil {
			if _, err := input.Apply(access.BaseConfig()); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		err := backend.SaveRuntimeSettings(ctx, input.RuntimeSettings, input.Version)
		switch {
		case errors.Is(err, store.ErrSettingsConflict):
			http.Error(w, "Settings changed; reload the page", 409)
		case err != nil:
			http.Error(w, "Unable to save settings", 503)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
}
