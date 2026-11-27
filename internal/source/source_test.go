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
