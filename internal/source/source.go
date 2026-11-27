package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
)

// Backend dimensions use structured metadata, never message or stack trace fields.
var dimensions = []string{"ip", "uri", "host", "ua", "status", "ip_uri", "ip_host", "ip_ua", "ip_status", "uri_status", "ip_method", "method", "waf", "wafrule", "tls", "upstream", "level", "exception", "dependency", "vulnerability"}

type Client struct {
	Config config.Source
	HTTP   *http.Client
}

type HTTPStatusError int

func (s HTTPStatusError) Error() string { return fmt.Sprintf("source HTTP status %d", s) }

func (c Client) request(ctx context.Context, method, path string, body any, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Config.URL, "/")+path, &b)
	if err != nil {
		return errors.New("invalid source request")
	}
	if secret := os.Getenv(c.Config.CredentialEnv); secret != "" {
		scheme := "Bearer"
		if c.Config.Username != "" {
			req.SetBasicAuth(c.Config.Username, secret)
		} else {
			if c.Config.Kind == "elasticsearch" {
				scheme = "ApiKey"
			}
			req.Header.Set("Authorization", scheme+" "+secret)
		}
	}
	if c.Config.Tenant != "" {
		req.Header.Set("X-Scope-OrgID", c.Config.Tenant)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		// Transport errors can contain the full upstream URL and LogQL selector.
		return errors.New("source transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return HTTPStatusError(resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	decoder.UseNumber()
	return decoder.Decode(out)
}

func (c Client) Aggregate(ctx context.Context, w platform.Window) (platform.Snapshot, error) {
	if c.Config.Kind == "loki" {
		return c.loki(ctx, w)
	}
	return c.elastic(ctx, w)
}

func (c Client) elastic(ctx context.Context, w platform.Window) (platform.Snapshot, error) {
	type object = map[string]any
	result := platform.Snapshot{Latency: map[string]map[string]float64{}}
	query := object{"range": object{c.Config.Fields["timestamp"]: object{"gte": w.Start, "lt": w.End}}}
	path := "/" + c.Config.Index + "/_search?allow_partial_search_results=false"
	var err error
	result.Total, result.OverallLatency, err = c.elasticWindow(ctx, path, query, c.Config.Fields["latency"])
	if err != nil || result.Total == 0 {
		return result, err
	}
	for _, dimension := range dimensions {
		names := strings.Split(dimension, "_")
		keys := []object{}
		for _, name := range names {
			if field := c.Config.Fields[name]; field != "" {
				keys = append(keys, object{name: object{"terms": object{"field": field}}})
			}
		}
		if len(keys) != len(names) {
			continue
		}
		composite := object{"size": 500, "sources": keys}
		agg := object{"composite": composite}
		if dimension == "uri" && c.Config.Fields["latency"] != "" {
			agg["aggs"] = object{"latency": object{"percentiles": object{"field": c.Config.Fields["latency"], "percents": []int{50, 95, 99}}}}
		}
		for page := 0; ; page++ {
			if page >= 1000 {
				return result, fmt.Errorf("composite page limit exceeded for %s", dimension)
			}
			var response struct {
				TimedOut     bool `json:"timed_out"`
				Aggregations map[string]struct {
					After   map[string]any `json:"after_key"`
					Buckets []struct {
						Key     map[string]any
						Count   int64 `json:"doc_count"`
						Latency struct{ Values map[string]*float64 }
					}
				}
			}
			if err := c.request(ctx, "POST", path, object{"size": 0, "track_total_hits": false, "query": query, "aggs": object{"groups": agg}}, &response); err != nil {
				return result, err
			}
			data, ok := response.Aggregations["groups"]
			if !ok || response.TimedOut {
				return result, fmt.Errorf("incomplete Elasticsearch aggregation")
			}
			for _, b := range data.Buckets {
				values := []string{}
				for _, name := range names {
					values = append(values, fmt.Sprint(b.Key[name]))
				}
				value := strings.Join(values, "\x00")
				result.Buckets = append(result.Buckets, platform.Bucket{Dimension: dimension, Value: value, Count: b.Count})
				for k, v := range b.Latency.Values {
					if v != nil {
						if result.Latency[value] == nil {
							result.Latency[value] = map[string]float64{}
						}
						result.Latency[value][k] = *v
					}
				}
			}
			if len(data.After) == 0 || len(data.Buckets) == 0 {
				break
			}
			composite["after"] = data.After
		}
	}
	return result, nil
}

func (c Client) elasticWindow(ctx context.Context, path string, query any, latency string) (int64, map[string]float64, error) {
	type object = map[string]any
	request := object{"size": 0, "track_total_hits": true, "query": query}
	if latency != "" {
		request["aggs"] = object{"latency": object{"percentiles": object{"field": latency, "percents": []int{50, 95, 99}}}}
	}
	var totals struct {
		TimedOut     bool `json:"timed_out"`
		Hits         struct{ Total struct{ Value int64 } }
		Aggregations struct {
			Latency struct{ Values map[string]*float64 }
		}
	}
	if err := c.request(ctx, "POST", path, request, &totals); err != nil {
		return 0, nil, err
	}
	if totals.TimedOut {
		return 0, nil, fmt.Errorf("Elasticsearch totals query timed out")
	}
	overall := map[string]float64{}
	for _, key := range []string{"50.0", "95.0", "99.0"} {
		if value := totals.Aggregations.Latency.Values[key]; value != nil && *value >= 0 {
			overall[key] = *value
		}
	}
	return totals.Hits.Total.Value, overall, nil
}

func (c Client) loki(ctx context.Context, w platform.Window) (platform.Snapshot, error) {
	result := platform.Snapshot{Latency: map[string]map[string]float64{}}
	duration := w.End.Sub(w.Start).String()
	evaluate := func(query string, consume func(map[string]string, float64)) error {
		return c.lokiMetric(ctx, w, query, consume)
	}
	if err := evaluate(fmt.Sprintf("sum(count_over_time(%s [%s]))", c.Config.Selector, duration), func(_ map[string]string, v float64) { result.Total = int64(v) }); err != nil {
		return result, err
	}
	for _, dimension := range dimensions {
		fields := []string{}
		for _, name := range strings.Split(dimension, "_") {
			if f := c.Config.Fields[name]; f != "" {
				fields = append(fields, f)
			}
		}
		if len(fields) != len(strings.Split(dimension, "_")) {
			continue
		}
		query := fmt.Sprintf("sum by (%s) (count_over_time(%s | json | __error__=\"\" [%s]))", strings.Join(fields, ","), c.Config.Selector, duration)
		// Extract only the mapped backend classification from the JSON record.
		switch dimension {
		case "level", "exception", "dependency", "vulnerability":
			query = fmt.Sprintf("sum by (sre_aiobs_classification) (count_over_time(%s | json sre_aiobs_classification=%q | __error__=\"\" [%s]))", c.Config.Selector, fields[0], duration)
			fields = []string{"sre_aiobs_classification"}
		}
		if err := evaluate(query, func(m map[string]string, v float64) {
			values := []string{}
			for _, field := range fields {
				if m[field] == "" {
					return
				}
				values = append(values, m[field])
			}
			result.Buckets = append(result.Buckets, platform.Bucket{Dimension: dimension, Value: strings.Join(values, "\x00"), Count: int64(v)})
		}); err != nil {
			return result, err
		}
	}
	uri, latency := c.Config.Fields["uri"], c.Config.Fields["latency"]
	if latency != "" {
		result.OverallLatency = map[string]float64{}
		for _, p := range []string{"50", "95", "99"} {
			// Empty grouping combines all matching streams before calculating the percentile.
			query := fmt.Sprintf("quantile_over_time(0.%s, %s | json | unwrap %s | __error__=\"\" [%s]) by ()", p, c.Config.Selector, latency, duration)
			samples := 0
			if err := evaluate(query, func(_ map[string]string, v float64) {
				samples++
				if v >= 0 {
					result.OverallLatency[p+".0"] = v
				}
			}); err != nil {
				return result, err
			}
			if samples > 1 {
				return result, fmt.Errorf("Loki returned multiple window latency aggregates")
			}
		}
	}

	if uri != "" && latency != "" {
		for _, p := range []string{"50", "95", "99"} {
			query := fmt.Sprintf("quantile_over_time(0.%s, %s | json | unwrap %s | __error__=\"\" [%s]) by (%s)", p, c.Config.Selector, latency, duration, uri)
			if err := evaluate(query, func(m map[string]string, v float64) {
				if result.Latency[m[uri]] == nil {
					result.Latency[m[uri]] = map[string]float64{}
				}
				result.Latency[m[uri]][p+".0"] = v
			}); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (c Client) lokiMetric(ctx context.Context, w platform.Window, query string, consume func(map[string]string, float64)) error {
	q := url.Values{"query": {query}, "time": {strconv.FormatInt(w.End.Add(-time.Nanosecond).UnixNano(), 10)}}
	var r struct {
		Status string
		Data   struct {
			Result []struct {
				Metric map[string]string
				Value  []json.RawMessage
			}
		}
	}
	if err := c.request(ctx, "GET", "/loki/api/v1/query?"+q.Encode(), nil, &r); err != nil {
		return err
	}
	if r.Status != "success" {
		return fmt.Errorf("Loki query failed")
	}
	for _, row := range r.Data.Result {
		var raw string
		if len(row.Value) != 2 || json.Unmarshal(row.Value[1], &raw) != nil {
			return fmt.Errorf("invalid Loki sample")
		}
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		if !math.IsNaN(number) && !math.IsInf(number, 0) {
			consume(row.Metric, number)
		}
	}
	return nil
}

// Check uses the same credentials, filter and read-only aggregate endpoint as the scheduler.
func (c Client) Check(ctx context.Context, end time.Time) (int64, error) {
	w := platform.Window{Start: end.Add(-time.Minute), End: end}
	if c.Config.Kind == "loki" {
		var total int64
		query := fmt.Sprintf("sum(count_over_time(%s [1m]))", c.Config.Selector)
		err := c.lokiMetric(ctx, w, query, func(_ map[string]string, v float64) { total = int64(v) })
		return total, err
	}
	query := map[string]any{"range": map[string]any{c.Config.Fields["timestamp"]: map[string]any{"gte": w.Start, "lt": w.End}}}
	total, _, err := c.elasticWindow(ctx, "/"+c.Config.Index+"/_search?allow_partial_search_results=false", query, "")
	return total, err
}
