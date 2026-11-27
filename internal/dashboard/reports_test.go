package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sre-aiobs/internal/platform"
	"sre-aiobs/internal/store"
)

type reportFixture struct{}

func (reportFixture) RunReport(context.Context, string) (store.RunReport, error) {
	return store.RunReport{
		Source:   "nginx",
		Analysis: &platform.Analysis{Summary: "<script>literal</script>"},
	}, nil
}

func TestRunReportFormats(t *testing.T) {
	mux := http.NewServeMux()
	mountReports(mux, reportFixture{}, func(h http.Handler) http.HandlerFunc { return h.ServeHTTP })
	for _, item := range []struct{ suffix, media, text string }{
		{"", "text/html", "&lt;script&gt;literal&lt;/script&gt;"},
		{"?format=html", "text/html", "暂无采集结果"},
		{"?format=json", "application/json", `"log_count":null`},
		{"?format=markdown", "text/markdown", "# 巡检报告"},
	} {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/reports/"+strings.Repeat("a", 32)+item.suffix, nil))
		if res.Code != 200 || !strings.HasPrefix(res.Header().Get("Content-Type"), item.media) ||
			!strings.Contains(res.Body.String(), item.text) || (item.suffix != "" && res.Header().Get("Content-Disposition") == "") {
			t.Fatalf("format %s: %d %s", item.suffix, res.Code, res.Body.String())
		}
	}
}
