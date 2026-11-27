package metrics

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sre-aiobs/internal/platform"
)

type Victoria struct {
	HTTP                     *http.Client
	WriteURL, ReadURL, Token string
	GrafanaURL               string
}

func ValidateURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("expected an HTTP(S) base URL without credentials, query, or fragment")
	}
	return nil
}

func (v Victoria) Write(ctx context.Context, j platform.Job, r platform.Report) error {
	high, maxScore := 0, 0
	for _, e := range r.Events {
		if e.Score >= 80 {
			high++
		}
		maxScore = max(maxScore, e.Score)
	}
	sample := fmt.Sprintf("{source=%q,kind=%q}", j.Source, j.Kind)
	at := j.Window.End.UnixMilli()
	body := fmt.Sprintf(
		"sre_aiobs_window_logs%s %d %d\nsre_aiobs_window_risk_events%s %d %d\nsre_aiobs_window_high_risk_events%s %d %d\nsre_aiobs_window_max_risk_score%s %d %d\nsre_aiobs_window_evaluated%s 1 %d\n",
		sample, r.Snapshot.Total, at, sample, len(r.Events), at, sample, high, at, sample, maxScore, at, sample, at,
	)
	var latency strings.Builder
	if seconds := j.Window.End.Sub(j.Window.Start).Seconds(); seconds > 0 {
		fmt.Fprintf(&latency, "sre_aiobs_window_qps%s %g %d\n", sample, float64(r.Snapshot.Total)/seconds, at)
	}
	for _, p := range []string{"50", "90", "95", "99"} {
		seconds, ok := r.Snapshot.OverallLatency[p+".0"]
		if !ok || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			continue
		}
		fmt.Fprintf(&latency, "sre_aiobs_window_latency_seconds{source=%q,kind=%q,quantile=%q} %g %d\n", j.Source, j.Kind, "0."+p, seconds, at)
		if p == "95" && r.LatencyThreshold > 0 && !math.IsInf(r.LatencyThreshold, 0) {
			breach := 0
			if seconds >= r.LatencyThreshold {
				breach = 1
			}
			fmt.Fprintf(&latency, "sre_aiobs_window_latency_threshold_seconds%s %g %d\nsre_aiobs_window_latency_p95_breach%s %d %d\n", sample, r.LatencyThreshold, at, sample, breach, at)
		}
	}
	body += latency.String()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(v.WriteURL, "/")+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("VictoriaMetrics write request is invalid")
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4")
	if v.Token != "" {
		req.Header.Set("Authorization", "Bearer "+v.Token)
	}
	client := *v.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("VictoriaMetrics write failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("VictoriaMetrics write HTTP status %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (v Victoria) Link(j platform.Job) string {
	if v.ReadURL == "" && v.GrafanaURL == "" {
		return ""
	}
	period := j.Window.End.Sub(j.Window.Start)
	if period < time.Minute || period > 24*time.Hour {
		return ""
	}
	if v.GrafanaURL != "" {
		query := url.Values{
			"var-source": {j.Source}, "var-kind": {j.Kind},
			"from": {strconv.FormatInt(j.Window.Start.Add(-3*period).UnixMilli(), 10)},
			"to":   {strconv.FormatInt(j.Window.End.Add(time.Second).UnixMilli(), 10)},
		}
		return strings.TrimRight(v.GrafanaURL, "/") + "/d/sre-aiobs/sre-aiobs?" + query.Encode()
	}

	parameters := url.Values{
		"nocache": {"1"},
		"query":   {fmt.Sprintf(`{__name__=~"sre_aiobs_window_(logs|qps|risk_events|high_risk_events|max_risk_score|evaluated|latency_seconds|latency_threshold_seconds|latency_p95_breach)",source=%q,kind=%q}`, j.Source, j.Kind)},
		"start":   {strconv.FormatInt(j.Window.Start.Add(-3*period).Unix(), 10)},
		"end":     {strconv.FormatInt(j.Window.End.Unix(), 10)},
		"step":    {strconv.FormatInt(int64(period/time.Second), 10)},
	}
	return strings.TrimRight(v.ReadURL, "/") + "/api/v1/query_range?" + parameters.Encode()
}
