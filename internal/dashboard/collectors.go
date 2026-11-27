package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"sre-aiobs/internal/collector"
	"sre-aiobs/internal/store"
)

type collectorStore interface {
	CollectorSettings(context.Context) (collector.Settings, int64, error)
	SaveCollectorSettings(context.Context, collector.Settings, int64) error
	BeginCollectorRun(context.Context, string, int64, int) (string, error)
	FinishCollectorRun(context.Context, string, string) error
	CollectorRuns(context.Context) ([]store.CollectorRun, error)
}

func mountCollectors(mux *http.ServeMux, backend collectorStore, access SourceAccess, protect func(http.Handler) http.HandlerFunc, password string) {
	const route = "/api/collectors"
	enabled := access.Collector.Enabled() && access.WorkContext != nil
	editable := access.SettingsEncrypted && password != ""
	expected := sha256.Sum256([]byte(password))
	write := func(w http.ResponseWriter, r *http.Request, value any) bool {
		supplied := sha256.Sum256([]byte(r.Header.Get("X-Alert-Settings-Password")))
		if !editable || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			http.Error(w, "Settings authorization and encryption required", 403)
			return false
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "JSON required", 415)
			return false
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(value) != nil || decoder.Decode(&extra) != io.EOF {
			http.Error(w, "Invalid request", 400)
			return false
		}
		return true
	}
	mux.HandleFunc("GET "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		var settings collector.Settings
		var version int64
		if access.SettingsEncrypted {
			var err error
			settings, version, err = backend.CollectorSettings(ctx)
			if err != nil {
				http.Error(w, "Saved settings unavailable", 503)
				return
			}
		}
		hasPassword := settings.Password != ""
		settings.Password = ""
		if settings.Hosts == nil {
			settings.Hosts = []collector.Host{}
		}
		runs, err := backend.CollectorRuns(ctx)
		if err != nil {
			http.Error(w, "Deployment history unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			collector.Settings
			Version            int64                `json:"version"`
			PasswordConfigured bool                 `json:"password_configured"`
			Editable           bool                 `json:"editable"`
			Enabled            bool                 `json:"enabled"`
			Runs               []store.CollectorRun `json:"runs"`
		}{settings, version, hasPassword, editable, enabled, runs})
	})))
	mux.HandleFunc("PUT "+route, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			collector.Settings
			Version int64 `json:"version"`
		}
		if !write(w, r, &input) {
			return
		}
		if input.Version < 0 {
			http.Error(w, "Invalid settings version", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		previous, version, err := backend.CollectorSettings(ctx)
		if err != nil {
			http.Error(w, "Saved settings unavailable", 503)
			return
		}
		if input.Version != version {
			http.Error(w, "Settings changed; reload the page", 409)
			return
		}
		if input.Password == "" {
			input.Password = previous.Password
		}
		if err := input.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch err := backend.SaveCollectorSettings(ctx, input.Settings, version); {
		case errors.Is(err, store.ErrSettingsConflict):
			http.Error(w, "Settings changed; reload the page", 409)
		case err != nil:
			http.Error(w, "Unable to save settings", 503)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
	mux.HandleFunc("POST "+route+"/runs", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Action  string `json:"action"`
			Version int64  `json:"version"`
		}
		if !write(w, r, &input) {
			return
		}
		if input.Action != "check" && input.Action != "deploy" {
			http.Error(w, "Invalid collector action", 400)
			return
		}
		if !enabled {
			http.Error(w, "Ansible controller is disabled", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		settings, version, err := backend.CollectorSettings(ctx)
		if err != nil {
			http.Error(w, "Saved settings unavailable", 503)
			return
		}
		if version == 0 || input.Version != version || settings.Validate() != nil {
			http.Error(w, "Save valid settings and reload before running", 409)
			return
		}
		id, err := backend.BeginCollectorRun(ctx, input.Action, version, len(settings.Hosts))
		if errors.Is(err, store.ErrCollectorBusy) {
			http.Error(w, "Another collector run is active", 409)
			return
		}
		if err != nil {
			http.Error(w, "Unable to start run", 503)
			return
		}
		go func() {
			runCtx, cancelRun := context.WithTimeout(access.WorkContext, collector.Timeout)
			defer cancelRun()
			status := "succeeded"
			if err := access.Collector.Run(runCtx, input.Action, settings); err != nil {
				status = "failed"
				if access.WorkContext.Err() != nil {
					status = "cancelled"
				}
			}
			finishCtx, cancelFinish := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancelFinish()
			if err := backend.FinishCollectorRun(finishCtx, id, status); err != nil {
				slog.Error("collector run status unavailable", "run_id", id)
			}
		}()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	})))
}
