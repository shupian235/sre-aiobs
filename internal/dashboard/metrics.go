package dashboard

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

type MetricsLink func(context.Context, string) (string, error)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func mountMetricsLink(mux *http.ServeMux, link MetricsLink, protect func(http.Handler) http.HandlerFunc) {
	mux.HandleFunc("GET /api/metrics-link/{run}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("run")
		if !runIDPattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		destination, err := link(ctx, id)
		if err != nil {
			http.Error(w, "Metrics link unavailable", 503)
			return
		}
		if destination == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, destination, http.StatusFound)
	})))
}
