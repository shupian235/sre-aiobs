package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/source"
)

// SourceAccess exposes only the current validated scheduler configuration.
type SourceAccess struct {
	Current func() []config.Source
	HTTP    *http.Client
}

func mountSources(mux *http.ServeMux, access SourceAccess, protect func(http.Handler) http.HandlerFunc) {
	slots := make(chan struct{}, 4)
	mux.HandleFunc("GET /api/sources", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type descriptor struct {
			Name            string `json:"name"`
			Kind            string `json:"kind"`
			Environment     string `json:"environment"`
			Backend         bool   `json:"backend"`
			Vulnerabilities bool   `json:"vulnerabilities,omitempty"`
		}
		sources := make([]descriptor, 0)
		for _, s := range access.Current() {
			sources = append(sources, descriptor{s.Name, s.Kind, s.Environment, s.Fields["level"] != "" || s.Fields["exception"] != "" || s.Fields["dependency"] != "", s.Fields["vulnerability"] != ""})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sources)
	})))
	mux.HandleFunc("POST /api/sources/{name}/check", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SRE-AIObs-Check") != "1" {
			http.Error(w, "Check header required", 400)
			return
		}
		var selected *config.Source
		for _, s := range access.Current() {
			if s.Name == r.PathValue("name") {
				selected = &s
				break
			}
		}
		if selected == nil {
			http.NotFound(w, r)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Source checks busy", 429)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		started := time.Now().UTC()
		count, err := (source.Client{Config: *selected, HTTP: access.HTTP}).Check(ctx, started)
		result := struct {
			Status     string    `json:"status"`
			Code       string    `json:"code,omitempty"`
			Count      *int64    `json:"count,omitempty"`
			DurationMS int64     `json:"duration_ms"`
			CheckedAt  time.Time `json:"checked_at"`
		}{Status: "ok", Count: &count, DurationMS: time.Since(started).Milliseconds(), CheckedAt: started}
		if err != nil {
			result.Status, result.Code, result.Count = "failed", "query_failed", nil
			var status source.HTTPStatusError
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				result.Code = "timeout"
			case errors.As(err, &status):
				switch status {
				case 401, 403:
					result.Code = "authorization_denied"
				case 404:
					result.Code = "endpoint_missing"
				case 429:
					result.Code = "upstream_busy"
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})))
}
