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
		for _, line := range []string{
			`sre_aiobs_window_qps{source="nginx-prod",kind="minute"} 0.5 `,
			`sre_aiobs_window_latency_seconds{source="nginx-prod",kind="minute",quantile="0.50"} 0.05 `,
			`sre_aiobs_window_latency_seconds{source="nginx-prod",kind="minute",quantile="0.95"} 0.12 `,
			`sre_aiobs_window_latency_seconds{source="nginx-prod",kind="minute",quantile="0.99"} 0.2 `,
			`sre_aiobs_window_latency_threshold_seconds{source="nginx-prod",kind="minute"} 0.1 `,
			`sre_aiobs_window_latency_p95_breach{source="nginx-prod",kind="minute"} 1 `,
		} {
			if !strings.Contains(string(data), line) {
				t.Errorf("missing %s", line)
			}
		}
		if !strings.Contains(string(data), expected) || strings.Contains(string(data), "fixture-private") || strings.Contains(string(data), job.ID) {
			t.Errorf("incorrect sample body: %s", data)
		}
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})}
	report := platform.Report{LatencyThreshold: 0.1, Snapshot: platform.Snapshot{Total: 30, OverallLatency: map[string]float64{"50.0": 0.05, "95.0": 0.12, "99.0": 0.2}}, Events: []platform.Event{{Score: 80, Target: "fixture-private"}, {Score: 20}}}
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

func TestGrafanaLinkUsesDashboardAndWindow(t *testing.T) {
	end := time.Unix(1800000000, 0).UTC()
	v := Victoria{ReadURL: "https://metrics.example", GrafanaURL: "https://grafana.example/grafana/"}
	j := platform.Job{Source: "nginx-test", Kind: "five_minutes", Window: platform.Window{Start: end.Add(-5 * time.Minute), End: end}}
	raw := v.Link(j)
	u, err := url.Parse(raw)
	if err != nil || u.Host != "grafana.example" || u.Path != "/grafana/d/sre-aiobs/sre-aiobs" || u.Query().Get("var-source") != "nginx-test" ||
		u.Query().Get("var-kind") != "five_minutes" || u.Query().Get("from") != "1799998800000" || u.Query().Get("to") != "1800000001000" {
		t.Fatalf("invalid Grafana link: %s", raw)
	}
}
