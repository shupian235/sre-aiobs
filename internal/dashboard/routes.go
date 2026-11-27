package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"sre-aiobs/internal/config"
)

type routeStore interface {
	SourceRoutes(context.Context, string) ([]config.Route, bool, error)
	SaveSourceRoutes(context.Context, string, []config.Route) error
}

func mountRoutes(mux *http.ServeMux, sources func() []config.Source, backend routeStore, protect func(http.Handler) http.HandlerFunc, password string) {
	const route = "/api/sources/{name}/routes"
	expected := sha256.Sum256([]byte(password))
	handler := func(w http.ResponseWriter, req *http.Request) {
		var source *config.Source
		for _, item := range sources() {
			if item.Name == req.PathValue("name") && item.Fields["uri"] != "" {
				source = &item
				break
			}
		}
		if source == nil {
			http.NotFound(w, req)
			return
		}

		ctx, cancel := context.WithTimeout(req.Context(), 4*time.Second)
		defer cancel()
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			routes, overridden, err := backend.SourceRoutes(ctx, source.Name)
			if err != nil {
				http.Error(w, "Route settings unavailable", http.StatusServiceUnavailable)
				return
			}
			if !overridden {
				routes = source.Routes
			}
			if routes == nil {
				routes = []config.Route{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Routes     []config.Route `json:"routes"`
				Overridden bool           `json:"overridden"`
				Editable   bool           `json:"editable"`
			}{routes, overridden, password != ""})
			return
		}

		supplied := sha256.Sum256([]byte(req.Header.Get("X-Alert-Settings-Password")))
		if password == "" || subtle.ConstantTimeCompare(supplied[:], expected[:]) != 1 {
			http.Error(w, "Route settings authorization required", http.StatusForbidden)
			return
		}
		var routes []config.Route
		if req.Method == http.MethodPut {
			mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
				return
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10))
			decoder.DisallowUnknownFields()
			var extra any
			if decoder.Decode(&routes) != nil || routes == nil || len(routes) > 100 || decoder.Decode(&extra) != io.EOF {
				http.Error(w, "Expected an array of up to 100 route rules", http.StatusBadRequest)
				return
			}
			for i := range routes {
				if len(routes[i].Pattern) > 1024 {
					http.Error(w, "Route pattern exceeds 1024 bytes", http.StatusBadRequest)
					return
				}
			}
			if err := config.ValidateRoutes(routes); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := backend.SaveSourceRoutes(ctx, source.Name, routes); err != nil {
			http.Error(w, "Unable to save routes", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		mux.HandleFunc(method+" "+route, protect(http.HandlerFunc(handler)))
	}
}
