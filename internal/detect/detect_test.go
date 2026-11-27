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

func TestNginxInspectionClassifiesSignalsAndKeepsRawTargetsOut(t *testing.T) {
	snapshot := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "ip_status", Value: "192.0.2.15\x00401", Count: 23},
		{Dimension: "ip_status", Value: "192.0.2.16\x00404", Count: 22},
		{Dimension: "ip_status", Value: "192.0.2.17\x00429", Count: 21},
		{Dimension: "ip_status", Value: "192.0.2.18\x00403", Count: 19},
		{Dimension: "uri_status", Value: "/orders?token=fixture-secret\x00502", Count: 20},
		{Dimension: "uri", Value: "/archive/file.bak?token=fixture-secret", Count: 1},
		{Dimension: "ip_method", Value: "192.0.2.19\x00UNEXPECTED", Count: 1},
		{Dimension: "method", Value: "UNEXPECTED", Count: 1},
		{Dimension: "waf", Value: "blocked", Count: 1},
		{Dimension: "wafrule", Value: "protocol_violation", Count: 1},
		{Dimension: "wafrule", Value: "untrusted fixture-secret", Count: 1},
		{Dimension: "tls", Value: "TLSv1.1", Count: 1},
		{Dimension: "tls", Value: "TLSv1.3", Count: 1},
		{Dimension: "upstream", Value: "502, 200", Count: 25},
	}}
	rules := config.Rules{NginxBurst: 20, Errors: 100, URI: 100, Latency: 1, BaselineMultiplier: 2}
	wants := map[string]string{"nginx_auth_failure_burst": "", "nginx_not_found_burst": "", "nginx_rate_limit_burst": "",
		"nginx_route_error": "/orders", "nginx_sensitive_artifact": BucketKey("/archive/file.bak")[:16], "nginx_unexpected_method": "",
		"nginx_waf_block": "", "nginx_waf_category": "protocol_violation", "nginx_legacy_tls": "", "nginx_upstream_error": ""}
	events := Evaluate(snapshot, nil, rules, nil, []config.Route{{Label: "/orders", Pattern: "^/orders$"}}, nil)
	for _, event := range events {
		target, ok := wants[event.Rule]
		if !ok || event.Target != target {
			t.Fatalf("unexpected event %+v", event)
		}
		delete(wants, event.Rule)
	}
	if len(wants) != 0 {
		t.Fatalf("missing checks: %v", wants)
	}
}

func TestOptionalBackendWarningsAndBusinessCodes(t *testing.T) {
	current := platform.Snapshot{Buckets: []platform.Bucket{
		{Dimension: "level", Value: "WARN", Count: 12},
		{Dimension: "businesscode", Value: "internal-callback-class", Count: 13},
		{Dimension: "businesscode", Value: "success", Count: 100},
	}}
	rules := config.Rules{BaselineMultiplier: 3}
	if got := Evaluate(current, nil, rules, nil, nil, nil); len(got) != 0 {
		t.Fatalf("optional rules enabled unexpectedly: %+v", got)
	}
	rules.BackendWarnings, rules.BusinessErrors = 10, 10
	events := Evaluate(current, nil, rules, nil, nil, nil)
	if len(events) != 2 {
		t.Fatalf("expected WARN and failed business-code events: %+v", events)
	}
	for _, event := range events {
		if event.Rule == "backend_business_error_spike" && event.Target != BucketKey("internal-callback-class")[:16] {
			t.Fatalf("business code was not hashed: %+v", event)
		}
	}
}
