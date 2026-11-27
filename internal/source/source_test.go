package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
)

func TestCompositeCursorAndReadOnlySearch(t *testing.T) {
	calls := 0
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/nginx/_search" {
			t.Error("unexpected API operation")
		}
		var body struct {
			Size *int
			Aggs map[string]struct {
				Composite struct{ After map[string]string }
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Size == nil || *body.Size != 0 {
			t.Error("search must request no hits")
		}
		switch calls {
		case 1:
			fmt.Fprint(w, `{"hits":{"total":{"value":3}}}`)
		case 2:
			fmt.Fprint(w, `{"aggregations":{"groups":{"after_key":{"ip":"192.0.2.2"},"buckets":[{"key":{"ip":"192.0.2.1"},"doc_count":2}]}}}`)
		case 3:
			if body.Aggs["groups"].Composite.After["ip"] != "192.0.2.2" {
				t.Error("did not use returned cursor")
			}
			fmt.Fprint(w, `{"aggregations":{"groups":{"buckets":[{"key":{"ip":"192.0.2.3"},"doc_count":1}]}}}`)
		default:
			t.Error("unexpected page")
		}
	}))
	client := Client{Config: config.Source{URL: "https://logs.test", Index: "nginx", Kind: "elasticsearch", Fields: map[string]string{"timestamp": "@timestamp", "ip": "ip"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := client.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 3 || len(snapshot.Buckets) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

type handlerTransport struct{ http.Handler }

func (h handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}
func testHTTP(h http.Handler) *http.Client {
	return &http.Client{Transport: handlerTransport{h}}
}

func TestElasticsearchBackendUsesMappedCompositeField(t *testing.T) {
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Size *int
			Aggs map[string]struct {
				Composite struct {
					Sources []map[string]struct{ Terms struct{ Field string } }
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Size == nil || *body.Size != 0 {
			t.Error("search must request no hits")
		}
		if len(body.Aggs) == 0 {
			fmt.Fprint(w, `{"hits":{"total":{"value":5}}}`)
			return
		}
		sources := body.Aggs["groups"].Composite.Sources
		if len(sources) != 1 || sources[0]["dependency"].Terms.Field != "dependency.failure.keyword" {
			t.Errorf("unexpected composite: %+v", sources)
		}
		fmt.Fprint(w, `{"aggregations":{"groups":{"buckets":[{"key":{"dependency":"payments_timeout"},"doc_count":5}]}}}`)
	}))
	c := Client{Config: config.Source{URL: "https://logs.test", Index: "orders", Kind: "elasticsearch", Fields: map[string]string{"timestamp": "@timestamp", "dependency": "dependency.failure.keyword"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := c.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 5 || len(snapshot.Buckets) != 1 || snapshot.Buckets[0] != (platform.Bucket{Dimension: "dependency", Value: "payments_timeout", Count: 5}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestWindowLatencyFromElasticsearch(t *testing.T) {
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			Aggs map[string]struct {
				Percentiles struct {
					Field    string
					Percents []int
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Error(err)
		}
		latency := query.Aggs["latency"].Percentiles
		if latency.Field != "request_time" || fmt.Sprint(latency.Percents) != "[50 90 95 99]" {
			t.Errorf("unexpected percentiles: %+v", latency)
		}
		fmt.Fprint(w, `{"hits":{"total":{"value":3}},"aggregations":{"latency":{"values":{"50.0":0.1,"95.0":0.3,"99.0":null}}}}`)
	}))
	c := Client{Config: config.Source{URL: "https://logs.test", Kind: "elasticsearch", Index: "app", Fields: map[string]string{"timestamp": "@timestamp", "latency": "request_time"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := c.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 3 || len(snapshot.OverallLatency) != 2 || snapshot.OverallLatency["95.0"] != 0.3 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestElasticsearchBasicAuthAndEmptyIndex(t *testing.T) {
	t.Setenv("TEST_ES_BASIC_SECRET", "fixture-password")
	calls := 0
	httpClient := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "sre_aiobs_reader" || pass != "fixture-password" {
			t.Error("missing Elasticsearch Basic credentials")
		}
		fmt.Fprint(w, `{"hits":{"total":{"value":0}}}`)
	}))
	client := Client{Config: config.Source{URL: "https://logs.test", Index: "nginx-access-*", Kind: "elasticsearch", Username: "sre_aiobs_reader", CredentialEnv: "TEST_ES_BASIC_SECRET", Fields: map[string]string{"timestamp": "@timestamp", "ip": "client_ip"}}, HTTP: httpClient}
	end := time.Now()
	snapshot, err := client.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 0 || calls != 1 {
		t.Fatalf("snapshot=%+v calls=%d err=%v", snapshot, calls, err)
	}
}

func TestElasticsearchIPBucketsIncludeOptionalGeoIP(t *testing.T) {
	calls := 0
	httpClient := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"hits":{"total":{"value":1}}}`)
			return
		}
		var query json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil || !strings.Contains(string(query), "client_geo.region_iso_code") {
			t.Fatal("missing region bucket")
		}
		fmt.Fprint(w, `{"aggregations":{"groups":{"buckets":[{"key":{"ip":"::ffff:8.8.8.8"},"doc_count":1,"countries":{"buckets":[{"key":"US","regions":{"buckets":[{"key":"US-CA"}]}}]}}]}}}`)
	}))
	client := Client{
		HTTP: httpClient,
		Config: config.Source{URL: "http://logs.test", Kind: "elasticsearch", Index: "nginx", Fields: map[string]string{
			"timestamp": "@timestamp", "ip": "client_ip", "country": "client_geo.country_iso_code", "region": "client_geo.region_iso_code",
		}},
	}
	end := time.Now()
	result, err := client.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || calls != 2 || result.Locations["8.8.8.8"] != (platform.Location{Country: "US", Region: "US-CA"}) {
		t.Fatalf("unexpected result: %+v, err=%v", result, err)
	}
}
