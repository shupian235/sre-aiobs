package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
)

type Geo func(netip.Addr) (string, string)

var cveID = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,12}$`)

var signatures = map[string]*regexp.Regexp{
	"sql_injection":  regexp.MustCompile(`(?i)\bunion\s+(all\s+)?select\b|\b(?:or|and)\s+['"]?\d+['"]?\s*=\s*['"]?\d+|\b(?:sleep|benchmark)\s*\(`),
	"xss":            regexp.MustCompile(`(?i)<\s*(script|iframe|svg)\b|javascript\s*:|\bon(?:error|load)\s*=`),
	"path_traversal": regexp.MustCompile(`(?:^|[/\\])\.\.(?:[/\\]|$)`),
}

func Evaluate(s platform.Snapshot, history []platform.Snapshot, rules config.Rules, hosts []string, routes []config.Route, geo Geo, allowedMethods ...string) []platform.Event {
	sensitive, agents := []*regexp.Regexp{}, []*regexp.Regexp{}
	for _, v := range rules.Sensitive {
		sensitive = append(sensitive, regexp.MustCompile(v))
	}
	for _, v := range rules.UserAgents {
		agents = append(agents, regexp.MustCompile(v))
	}
	routePatterns := make([]*regexp.Regexp, 0, len(routes))
	for _, route := range routes {
		routePatterns = append(routePatterns, regexp.MustCompile(route.Pattern))
	}
	routeTarget := func(path string) string {
		for i, pattern := range routePatterns {
			if pattern.MatchString(path) {
				return routes[i].Label
			}
		}
		return BucketKey(path)[:16]
	}

	allowed := map[string]bool{}
	for _, host := range hosts {
		allowed[strings.ToLower(host)] = true
	}
	baseline := map[string]float64{}
	for _, previous := range history {
		for _, b := range previous.Buckets {
			baseline[b.Dimension+"\x00"+b.Value] += float64(b.Count) / float64(len(history))
		}
	}
	events := []platform.Event{}
	correlated := map[string]map[string]int{}
	vulnerabilities := map[string]int64{}
	add := func(rule, ip string, count int64, score int) {
		event := platform.Event{Rule: rule, Count: count, Score: score}
		if addr, err := netip.ParseAddr(ip); err == nil && addr.Zone() == "" {
			event.IP = addr.Unmap().String()
			if geo != nil {
				event.Country, event.Region = geo(addr.Unmap())
			}
			if correlated[event.IP] == nil {
				correlated[event.IP] = map[string]int{}
			}
			if correlated[event.IP][rule] < score {
				correlated[event.IP][rule] = score
			}
		}
		events = append(events, event)
	}
	hasIPMethod := false
	for _, bucket := range s.Buckets {
		if bucket.Dimension == "ip_method" {
			hasIPMethod = true
			break
		}
	}
	for _, b := range s.Buckets {
		ip, value := "", b.Value
		dimension := b.Dimension
		if strings.HasPrefix(dimension, "ip_") {
			ip, value, _ = strings.Cut(value, "\x00")
			dimension = strings.TrimPrefix(dimension, "ip_")
		}
		exceeds := func(floor int64) bool {
			return b.Count >= floor && float64(b.Count) >= baseline[b.Dimension+"\x00"+BucketKey(b.Value)]*rules.BaselineMultiplier
		}
		switch dimension {
		case "vulnerability":
			id := strings.ToUpper(strings.TrimSpace(value))
			if cveID.MatchString(id) {
				vulnerabilities[id] += b.Count
			}

		case "level":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "fatal", "panic", "critical", "emergency", "alert", "crit", "emerg":
				add("backend_critical_log", "", b.Count, 95)
			case "warn", "warning":
				if rules.BackendWarnings > 0 && exceeds(rules.BackendWarnings) {
					add("backend_warning_spike", "", b.Count, 65)
				}
			case "error", "err":
				if exceeds(rules.BackendErrors) {
					add("backend_error_spike", "", b.Count, 80)
				}
			}
		case "exception", "dependency", "businesscode":
			// These fields contain a classification only when a failure occurred.
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "", "-", "null", "none", "false", "0", "ok", "success", "unknown":
				continue
			}
			if dimension == "exception" {
				add("backend_exception", "", b.Count, 85)
			} else if dimension == "businesscode" {
				if rules.BusinessErrors == 0 || !exceeds(rules.BusinessErrors) {
					continue
				}
				add("backend_business_error_spike", "", b.Count, 75)
			} else if exceeds(rules.DependencyErrors) {
				add("backend_dependency_failure", "", b.Count, 85)
			} else {
				continue
			}
			events[len(events)-1].Target = BucketKey(value)[:16]

		case "ip":
			if exceeds(rules.IP) {
				add("ip_frequency", value, b.Count, 45)
			}
		case "uri":
			decoded := value
			for i := 0; i < 2; i++ {
				next, err := url.QueryUnescape(decoded)
				if err != nil || next == decoded {
					break
				}
				decoded = next
			}
			path := uriPath(value)
			if ip == "" && exceeds(rules.URI) {
				add("uri_frequency", "", b.Count, 35)
				events[len(events)-1].Target = routeTarget(path)
			}
			for _, pattern := range sensitive {
				if pattern.MatchString(path) {
					add("sensitive_uri", ip, b.Count, 65)
					events[len(events)-1].Target = routeTarget(path)
					break
				}
			}
			if sensitiveArtifact.MatchString(path) {
				add("nginx_sensitive_artifact", ip, b.Count, 85)
				events[len(events)-1].Target = routeTarget(path)
			}

			for rule, pattern := range signatures {
				if pattern.MatchString(decoded) {
					add(rule, ip, b.Count, 85)
					events[len(events)-1].Target = routeTarget(path)
				}
			}
		case "host":
			if len(allowed) != 0 && !allowed[strings.ToLower(value)] {
				add("unexpected_host", ip, b.Count, 60)
			}
		case "ua":
			for _, pattern := range agents {
				if pattern.MatchString(value) {
					add("unexpected_user_agent", ip, b.Count, 50)
					break
				}
			}
		case "method":
			if unexpectedMethod(value, allowedMethods) && (b.Dimension == "ip_method" || !hasIPMethod) {
				add("nginx_unexpected_method", ip, b.Count, 85)
			}
		case "waf":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "block", "blocked", "deny", "denied", "drop", "dropped", "reject", "rejected":
				add("nginx_waf_block", "", b.Count, 85)
			}
		case "wafrule":
			if category := wafCategory(value); category != "" {
				add("nginx_waf_category", "", b.Count, 85)
				events[len(events)-1].Target = category
			}
		case "tls":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "sslv2", "sslv3", "tlsv1", "tlsv1.0", "tlsv1.1", "tls1.0", "tls1.1":
				add("nginx_legacy_tls", "", b.Count, 65)
			}
		case "upstream":
			if exceeds(rules.NginxBurst) && hasUpstreamError(value) {
				add("nginx_upstream_error", "", b.Count, 75)
			}
		case "uri_status":
			uri, status, ok := strings.Cut(b.Value, "\x00")
			if ok && exceeds(rules.NginxBurst) && (status == "429" || status == "502" || status == "503" || status == "504") {
				add("nginx_route_error", "", b.Count, 75)
				events[len(events)-1].Target = routeTarget(uriPath(uri))
			}

		case "status":
			if ip != "" && exceeds(rules.NginxBurst) {
				switch value {
				case "401", "403":
					add("nginx_auth_failure_burst", ip, b.Count, 80)
				case "404":
					add("nginx_not_found_burst", ip, b.Count, 75)
				case "429":
					add("nginx_rate_limit_burst", ip, b.Count, 80)
				}
			}
			if len(value) == 3 && (value[0] == '4' || value[0] == '5') && exceeds(rules.Errors) {
				add("http_status", ip, b.Count, 50)
			}
		}
	}
	for id, count := range vulnerabilities {
		events = append(events, platform.Event{Rule: platform.VulnerabilityRule, Target: id, Count: count, Score: 80})
	}
	for uri, values := range s.Latency {
		if values["95.0"] >= rules.Latency {
			add("latency_p95", "", 1, 40)
			events[len(events)-1].Target = BucketKey(uri)[:16]
		}
	}
	for ip, signals := range correlated {
		if len(signals) < 2 {
			continue
		}
		e := platform.Event{Rule: "correlated", IP: ip}
		for rule, score := range signals {
			e.Signals = append(e.Signals, rule)
			e.Score += score
		}
		sort.Strings(e.Signals)
		if e.Score > 100 {
			e.Score = 100
		}
		if geo != nil {
			e.Country, e.Region = geo(netip.MustParseAddr(ip))
		}
		events = append(events, e)
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.IP != b.IP {
			return a.IP < b.IP
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Target < b.Target
	})
	return events
}

func BucketKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
