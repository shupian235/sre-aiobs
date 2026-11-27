package detect

import (
	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
	"testing"
)

func TestCorrelationAndBaseline(t *testing.T) {
	s := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "ip", Value: "192.0.2.1", Count: 200},
		{Dimension: "ip_uri", Value: "192.0.2.1\x00/admin?token=secret", Count: 5},
		{Dimension: "ip_host", Value: "192.0.2.1\x00unknown", Count: 10},
	}}
	previous := platform.Snapshot{Buckets: []platform.Bucket{{Dimension: "ip", Value: BucketKey("192.0.2.1"), Count: 200}}}
	e := Evaluate(s, []platform.Snapshot{previous}, config.Rules{IP: 100, URI: 100, Errors: 10, Latency: 1, BaselineMultiplier: 2, Sensitive: []string{"^/admin$"}}, []string{"example.org"}, nil, nil)
	if len(e) != 3 || e[0].Rule != "correlated" || e[0].Score != 100 || e[0].IP != "192.0.2.1" {
		t.Fatalf("events = %#v", e)
	}
}

func TestBackendDetections(t *testing.T) {
	current := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "level", Value: "FATAL", Count: 1},
		{Dimension: "level", Value: "error", Count: 23},
		{Dimension: "level", Value: "info", Count: 400},
		{Dimension: "exception", Value: "UnhandledExampleError", Count: 2},
		{Dimension: "exception", Value: "null", Count: 1},
		{Dimension: "dependency", Value: "db_timeout", Count: 7},
		{Dimension: "dependency", Value: "api_timeout", Count: 5},
	}}
	history := []platform.Snapshot{{Buckets: []platform.Bucket{
		{Dimension: "dependency", Value: BucketKey("api_timeout"), Count: 4},
	}}}
	rules := config.Rules{BackendErrors: 20, DependencyErrors: 5, BaselineMultiplier: 2, Latency: 1}
	got := Evaluate(current, history, rules, nil, nil, nil)
	if len(got) != 4 {
		t.Fatalf("unexpected events: %+v", got)
	}
	want := map[string]string{"backend_critical_log": "", "backend_error_spike": "", "backend_exception": BucketKey("UnhandledExampleError")[:16], "backend_dependency_failure": BucketKey("db_timeout")[:16]}
	for _, e := range got {
		target, ok := want[e.Rule]
		if !ok || e.Target != target || e.Score < 80 || e.IP != "" {
			t.Fatalf("unexpected event: %+v", e)
		}
		delete(want, e.Rule)
	}
}

func TestRiskRouteTargetsUseConfiguredLabelsOrHashes(t *testing.T) {
	snapshot := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "uri", Value: "/admin?token=fixture-only", Count: 20},
		{Dimension: "ip_uri", Value: "192.0.2.7\x00/private-secret?q=%3Cscript%3E", Count: 3},
	}}
	rules := config.Rules{URI: 10, Latency: 1, Sensitive: []string{"^/admin$"}}
	events := Evaluate(snapshot, nil, rules, nil, []config.Route{{Label: "/admin", Pattern: "^/admin$"}}, nil)
	if len(events) != 3 {
		t.Fatalf("events=%+v", events)
	}
	for _, e := range events {
		if e.Rule == "xss" {
			if e.Target != BucketKey("/private-secret")[:16] {
				t.Fatalf("unexpected XSS target: %q", e.Target)
			}
		} else if e.Target != "/admin" {
			t.Fatalf("unexpected named route: %q", e.Target)
		}
	}
}

func TestVulnerabilityFindingsUseValidatedIDs(t *testing.T) {
	snapshot := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "vulnerability", Value: "CVE-2026-0001", Count: 3},
		{Dimension: "vulnerability", Value: "cve-2026-0001", Count: 1},
		{Dimension: "vulnerability", Value: "CVE-2026-0002 token=secret", Count: 5},
		{Dimension: "level", Value: "info", Count: 5},
	}}
	events := Evaluate(snapshot, nil, config.Rules{Latency: 1}, nil, nil, nil)
	if len(events) != 1 || events[0].Rule != platform.VulnerabilityRule || events[0].Target != "CVE-2026-0001" || events[0].Count != 4 {
		t.Fatalf("findings=%+v", events)
	}
}
