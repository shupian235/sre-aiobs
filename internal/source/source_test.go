package source

import (
	"context"
	"encoding/json"
	"errors"
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

func TestLokiUsesInstantMetricQuery(t *testing.T) {
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/loki/api/v1/query" || !strings.Contains(r.URL.Query().Get("query"), "count_over_time") {
			t.Error("unexpected API operation")
		}
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"client_ip":"192.0.2.1"},"value":[0,"2"]}]}}`)
	}))
	client := Client{Config: config.Source{URL: "https://logs.test", Kind: "loki", Selector: `{app="nginx"}`, Fields: map[string]string{"ip": "client_ip"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := client.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 2 || len(snapshot.Buckets) != 1 {
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

func TestLokiBackendExtractsOnlyConfiguredClassification(t *testing.T) {
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "sum by") {
			if !strings.Contains(q, `| json sre_aiobs_classification="exception_type" |`) {
				t.Errorf("unexpected parser: %s", q)
			}
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"sre_aiobs_classification":"OrderError"},"value":[0,"2"]}]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{},"value":[0,"5"]}]}}`)
	}))
	c := Client{Config: config.Source{URL: "https://logs.test", Kind: "loki", Selector: `{app="orders"}`, Fields: map[string]string{"exception": "exception_type"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := c.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || snapshot.Total != 5 || len(snapshot.Buckets) != 1 || snapshot.Buckets[0] != (platform.Bucket{Dimension: "exception", Value: "OrderError", Count: 2}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
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

func TestLokiWindowLatencyCombinesStreams(t *testing.T) {
	calls := 0
	clientHTTP := testHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		query := r.URL.Query().Get("query")
		if calls == 1 {
			if !strings.Contains(query, "count_over_time") {
				t.Errorf("unexpected total query: %s", query)
			}
			fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{},"value":[0,"3"]}]}}`)
			return
		}
		percentiles := []string{"50", "90", "95", "99"}
		if calls > 5 || !strings.HasPrefix(query, "quantile_over_time(0."+percentiles[calls-2]+",") || !strings.HasSuffix(query, "by ()") || !strings.Contains(query, " unwrap request_time ") {
			t.Errorf("unexpected percentile query: %s", query)
		}
		fmt.Fprintf(w, `{"status":"success","data":{"result":[{"metric":{},"value":[0,"0.%d"]}]}}`, calls)
	}))
	c := Client{Config: config.Source{URL: "https://logs.test", Kind: "loki", Selector: `{app="nginx"}`, Fields: map[string]string{"latency": "request_time"}}, HTTP: clientHTTP}
	end := time.Now()
	snapshot, err := c.Aggregate(context.Background(), platform.Window{Start: end.Add(-time.Minute), End: end})
	if err != nil || calls != 5 || snapshot.OverallLatency["50.0"] != 0.2 || snapshot.OverallLatency["90.0"] != 0.3 || snapshot.OverallLatency["95.0"] != 0.4 || snapshot.OverallLatency["99.0"] != 0.5 {
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

type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func TestLokiTransportErrorDoesNotExposeQuery(t *testing.T) {
	client := Client{Config: config.Source{Kind: "loki", URL: "https://private.test", Selector: `{app="private-selector"}`}}
	client.HTTP = &http.Client{Transport: failingTransport{errors.New("private-upstream-error")}}
	_, err := client.Check(context.Background(), time.Now())
	if err == nil || err.Error() != "source transport failed" {
		t.Fatalf("expected sanitized transport error, got %v", err)
	}

	client.HTTP.Transport = failingTransport{context.DeadlineExceeded}
	_, err = client.Check(context.Background(), time.Now())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout classification, got %v", err)
	}
}
