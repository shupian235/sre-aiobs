package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
)

// Backend dimensions use structured metadata, never message or stack trace fields.
var dimensions = []string{"ip", "uri", "host", "ua", "status", "ip_uri", "ip_host", "ip_ua", "ip_status", "uri_status", "ip_method", "method", "waf", "wafrule", "tls", "upstream", "level", "exception", "dependency", "businesscode", "vulnerability"}

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
		if c.Config.Username != "" {
			req.SetBasicAuth(c.Config.Username, secret)
		} else {
			req.Header.Set("Authorization", "ApiKey "+secret)
		}
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
		// Transport errors can contain the full upstream URL.
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
			agg["aggs"] = object{"latency": object{"percentiles": object{"field": c.Config.Fields["latency"], "percents": []int{50, 90, 95, 99}}}}
		}
		if country := c.Config.Fields["country"]; dimension == "ip" && country != "" {
			countries := object{"terms": object{"field": country, "size": 1}}
			if region := c.Config.Fields["region"]; region != "" {
				countries["aggs"] = object{"regions": object{"terms": object{"field": region, "size": 1}}}
			}
			agg["aggs"] = object{"countries": countries}
			result.Locations = map[string]platform.Location{}
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
						Key       map[string]any
						Count     int64 `json:"doc_count"`
						Latency   struct{ Values map[string]*float64 }
						Countries struct {
							Buckets []struct {
								Key     string
								Regions struct{ Buckets []struct{ Key string } }
							}
						}
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
				if result.Locations != nil && dimension == "ip" && len(b.Countries.Buckets) > 0 {
					country := b.Countries.Buckets[0]
					location := platform.Location{Country: country.Key}
					if len(country.Regions.Buckets) > 0 {
						location.Region = country.Regions.Buckets[0].Key
					}
					if ip, err := netip.ParseAddr(value); err == nil && ip.Zone() == "" {
						result.Locations[ip.Unmap().String()] = location
					}
				}
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
		request["aggs"] = object{"latency": object{"percentiles": object{"field": latency, "percents": []int{50, 90, 95, 99}}}}
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
	for _, key := range []string{"50.0", "90.0", "95.0", "99.0"} {
		if value := totals.Aggregations.Latency.Values[key]; value != nil && *value >= 0 {
			overall[key] = *value
		}
	}
	return totals.Hits.Total.Value, overall, nil
}

// Check uses the same credentials, filter and read-only aggregate endpoint as the scheduler.
func (c Client) Check(ctx context.Context, end time.Time) (int64, error) {
	w := platform.Window{Start: end.Add(-time.Minute), End: end}
	query := map[string]any{"range": map[string]any{c.Config.Fields["timestamp"]: map[string]any{"gte": w.Start, "lt": w.End}}}
	total, _, err := c.elasticWindow(ctx, "/"+c.Config.Index+"/_search?allow_partial_search_results=false", query, "")
	return total, err
}
