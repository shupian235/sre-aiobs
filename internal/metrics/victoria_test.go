package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"sre-aiobs/internal/platform"
)

type testTransport func(*http.Request) (*http.Response, error)

func (t testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func TestVictoriaWriteAndWindowLink(t *testing.T) {
	end := time.Unix(1800000000, 0).UTC()
	job := platform.Job{ID: "sensitive-run-id", Source: "nginx-prod", Kind: "minute", Window: platform.Window{Start: end.Add(-time.Minute), End: end}}
	v := Victoria{WriteURL: "https://write.example/insert/0/prometheus/", ReadURL: "https://read.example/select/0/prometheus", Token: "fixture-token"}
	v.HTTP = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if r.Method != "POST" || r.URL.Path != "/insert/0/prometheus/api/v1/import/prometheus" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("incorrect write request")
		}
		expected := fmt.Sprintf(`sre_aiobs_window_high_risk_events{source="nginx-prod",kind="minute"} 1 %d`, end.UnixMilli())
		if !strings.Contains(string(data), expected) || strings.Contains(string(data), "fixture-private") || strings.Contains(string(data), job.ID) {
			t.Errorf("incorrect sample body: %s", data)
		}
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})}
	report := platform.Report{Snapshot: platform.Snapshot{Total: 30}, Events: []platform.Event{{Score: 80, Target: "fixture-private"}, {Score: 20}}}
	if err := v.Write(context.Background(), job, report); err != nil {
		t.Fatal(err)
	}
	raw := v.Link(job)
	link, err := url.Parse(raw)
	if err != nil || link.Path != "/select/0/prometheus/api/v1/query_range" || link.Query().Get("end") != "1800000000" || link.Query().Get("step") != "60" || link.Query().Get("nocache") != "1" ||
		!strings.Contains(link.Query().Get("query"), `source="nginx-prod",kind="minute"`) || strings.Contains(raw, "fixture-token") {
		t.Fatalf("invalid range link: %s", raw)
	}
	if err := ValidateURL("https://user:password@example.test"); err == nil {
		t.Fatal("accepted credentials in URL")
	}
}
