package dashboard

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"sre-aiobs/internal/store"
)

//go:embed web/*
var assets embed.FS

type Reader interface {
	Dashboard(context.Context, string, string, time.Time, int) (store.Dashboard, error)
	DashboardSources(context.Context) ([]string, error)
	BackendLogs(context.Context, string, string, time.Time, int) (store.BackendLogs, error)
	Vulnerabilities(context.Context, string, string, time.Time, int) (store.BackendLogs, error)
	NginxRiskRoutes(context.Context, string, string, time.Time, int) (store.BackendLogs, error)
}

func Mount(mux *http.ServeMux, reader Reader, password string, access SourceAccess) {
	files, err := fs.Sub(assets, "web")
	if err != nil {
		panic(err)
	}
	expected := sha256.Sum256([]byte(password))
	if access.Login != nil {
		access.Login.mount(mux, expected)
	}
	protected := func(next http.Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			dashboardHeaders(w)
			if access.Login != nil {
				if access.Login.authenticate(w, r) {
					w.Header().Set("X-SRE-AIObs-Login", "/login")
					next.ServeHTTP(w, r)
				}
				return
			}
			if password == "" {
				http.Error(w, "Dashboard requires DASHBOARD_PASSWORD", http.StatusServiceUnavailable)
				return
			}
			username, secret, ok := r.BasicAuth()
			if !ok || !credentialsMatch(username, secret, expected) {
				w.Header().Set("WWW-Authenticate", `Basic realm="SRE AIObs", charset="UTF-8"`)
				http.Error(w, "Authentication required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
	}
	if backend, ok := reader.(reportStore); ok {
		mountReports(mux, backend, protected)
	}
	if backend, ok := reader.(alertStore); ok {
		mountAlerts(mux, backend, protected, os.Getenv("ALERT_SETTINGS_PASSWORD"), access.TelegramDestination)
	}
	if access.Current != nil {
		mountSources(mux, access, protected)
		if backend, ok := reader.(routeStore); ok {
			mountRoutes(mux, access.Current, backend, protected, os.Getenv("ALERT_SETTINGS_PASSWORD"))
		}
	}
	static := protected(http.FileServer(http.FS(files)))
	for _, route := range []string{"GET /{$}", "GET /app.css", "GET /app.js"} {
		mux.HandleFunc(route, static)
	}
	mux.HandleFunc("GET /api/dashboard", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters, ok := parseFilters(r.URL.Query())
		if !ok {
			http.Error(w, "Invalid time range or score filter", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		updated := time.Now().UTC()
		data, err := reader.Dashboard(ctx, filters.source, filters.kind, updated.Add(-filters.lookback), filters.minimum)
		if err != nil {
			http.Error(w, "Dashboard data unavailable", 503)
			return
		}
		sources, err := reader.DashboardSources(ctx)
		if err != nil {
			http.Error(w, "Dashboard data unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			store.Dashboard
			Sources []string  `json:"sources"`
			Updated time.Time `json:"updated"`
			Kind    string    `json:"kind"`
		}{data, sources, updated, filters.kind})
	})))
	mountEvents := func(route string, query func(context.Context, string, string, time.Time, int) (store.BackendLogs, error)) {
		mux.HandleFunc(route, protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			filters, ok := parseFilters(r.URL.Query())
			if !ok {
				http.Error(w, "Invalid time range or score filter", 400)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			updated := time.Now().UTC()
			data, err := query(ctx, filters.source, filters.kind, updated.Add(-filters.lookback), filters.minimum)
			if err != nil {
				http.Error(w, "Risk events unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				store.BackendLogs
				Updated time.Time `json:"updated"`
				Kind    string    `json:"kind"`
			}{data, updated, filters.kind})
		})))
	}
	mountEvents("GET /api/backend-logs", reader.BackendLogs)
	mountEvents("GET /api/vulnerabilities", reader.Vulnerabilities)
	mountEvents("GET /api/nginx-risk-routes", reader.NginxRiskRoutes)
}

type filters struct {
	source, kind string
	lookback     time.Duration
	minimum      int
}

func parseFilters(q url.Values) (filters, bool) {
	hours, minimum := 24, 0
	if value := q.Get("hours"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || (n != 1 && n != 6 && n != 24 && n != 168) {
			return filters{}, false
		}
		hours = n
	}
	if value := q.Get("score"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || (n != 0 && n != 65 && n != 80) {
			return filters{}, false
		}
		minimum = n
	}
	kind := "hour"
	if hours <= 6 {
		kind = "five_minutes"
	}
	return filters{q.Get("source"), kind, time.Duration(hours) * time.Hour, minimum}, true
}

func dashboardHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
