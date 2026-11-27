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
	Login           *Login
	MetricsLink     MetricsLink
	Current         func() []config.Source
	HTTP            *http.Client
	CheckCloudWatch func(context.Context, string) error
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
			Routes          bool   `json:"routes,omitempty"`
		}
		sources := make([]descriptor, 0)
		for _, s := range access.Current() {
			sources = append(sources, descriptor{s.Name, s.Kind, s.Environment, s.Fields["level"] != "" || s.Fields["exception"] != "" || s.Fields["dependency"] != "" || s.Fields["businesscode"] != "", s.Fields["vulnerability"] != "", s.Fields["uri"] != ""})
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
		timeout := selected.CheckTimeout
		if timeout == 0 {
			timeout = 3 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		started := time.Now().UTC()
		client := source.Client{Config: *selected, HTTP: access.HTTP}
		var count int64
		var err error
		for attempt := 0; ; attempt++ {
			switch {
			case selected.IsLogSource():
				count, err = client.Check(ctx, started)
			case selected.Kind == "cloudwatch" && access.CheckCloudWatch != nil:
				err = access.CheckCloudWatch(ctx, selected.Region)
			default:
				err = client.Monitor(ctx)
			}
			var status source.HTTPStatusError
			if err == nil || ctx.Err() != nil || attempt >= selected.CheckRetries ||
				(errors.As(err, &status) && status < 500 && status != 429) {
				break
			}
			timer := time.NewTimer(time.Duration(attempt+1) * 150 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
		}
		result := struct {
			Status     string    `json:"status"`
			Code       string    `json:"code,omitempty"`
			Count      *int64    `json:"count,omitempty"`
			DurationMS int64     `json:"duration_ms"`
			CheckedAt  time.Time `json:"checked_at"`
		}{Status: "ok", DurationMS: time.Since(started).Milliseconds(), CheckedAt: started}
		if selected.IsLogSource() {
			result.Count = &count
		}
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
