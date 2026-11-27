package metrics

import (
	"context"
	"fmt"
	"io"
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
	if v.ReadURL == "" {
		return ""
	}
	period := j.Window.End.Sub(j.Window.Start)
	if period < time.Minute || period > 24*time.Hour {
		return ""
	}
	parameters := url.Values{
		"nocache": {"1"},
		"query":   {fmt.Sprintf(`{__name__=~"sre_aiobs_window_(logs|risk_events|high_risk_events|max_risk_score|evaluated)",source=%q,kind=%q}`, j.Source, j.Kind)},
		"start":   {strconv.FormatInt(j.Window.Start.Add(-3*period).Unix(), 10)},
		"end":     {strconv.FormatInt(j.Window.End.Unix(), 10)},
		"step":    {strconv.FormatInt(int64(period/time.Second), 10)},
	}
	return strings.TrimRight(v.ReadURL, "/") + "/api/v1/query_range?" + parameters.Encode()
}
