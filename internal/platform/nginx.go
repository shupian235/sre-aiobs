package platform

// NginxRules lists the signals exposed by the Nginx inspection query.
func NginxRules() []string {
	return []string{
		"sensitive_uri", "uri_frequency", "sql_injection", "xss", "path_traversal",
		"ip_frequency", "unexpected_host", "unexpected_user_agent", "http_status", "latency_p95", "correlated",
		"nginx_sensitive_artifact", "nginx_unexpected_method", "nginx_waf_block", "nginx_waf_category", "nginx_legacy_tls",
		"nginx_upstream_error", "nginx_route_error", "nginx_auth_failure_burst", "nginx_not_found_burst", "nginx_rate_limit_burst",
	}
}
