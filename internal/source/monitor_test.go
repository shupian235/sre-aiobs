package source

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"sre-aiobs/internal/config"
)

func TestMonitorUsesReadOnlyEndpoints(t *testing.T) {
	for _, item := range []struct{ kind, path, query, body string }{
		{"prometheus", "/api/v1/query", "vector(1)", `{"status":"success"}`},
		{"victoriametrics", "/api/v1/query", "vector(1)", `{"status":"success"}`},
		{"kubernetes", "/version", "", `{"gitVersion":"v1.32.0"}`},
	} {
		t.Run(item.kind, func(t *testing.T) {
			client := Client{Config: config.Source{Kind: item.kind, URL: "https://example.test"}}
			client.HTTP = testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != item.path || r.URL.Query().Get("query") != item.query {
					t.Error("unexpected monitoring request")
				}
				fmt.Fprint(w, item.body)
			}))
			if err := client.Monitor(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
