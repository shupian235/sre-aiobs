package detect

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var sensitiveArtifact = regexp.MustCompile(`(?i)(?:^|/)(?:\.git|\.svn|\.hg|\.env(?:\.[a-z0-9_-]+)?|\.htpasswd|composer\.(?:json|lock)|package-lock\.json|server-status|actuator/(?:env|heapdump|configprops))(?:/|$)|\.(?:sql|bak|old|pem|key|swp)$`)

var defaultMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

func unexpectedMethod(value string, allowed []string) bool {
	method := strings.ToUpper(strings.TrimSpace(value))
	if method == "" || method == "-" {
		return false
	}
	if len(allowed) == 0 {
		allowed = defaultMethods
	}
	return !slices.Contains(allowed, method)
}

func uriPath(uri string) string {
	path, _, _ := strings.Cut(uri, "?")
	for i := 0; i < 2; i++ {
		next, err := url.PathUnescape(path)
		if err != nil || next == path {
			break
		}
		path = next
	}
	return path
}

func hasUpstreamError(value string) bool {
	for _, status := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ':' }) {
		switch strings.TrimSpace(status) {
		case "500", "502", "503", "504":
			return true
		}
	}
	return false
}

func wafCategory(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "sql_injection", "xss", "path_traversal", "ssrf", "rce", "command_injection", "xxe",
		"template_injection", "file_inclusion", "request_smuggling", "csrf", "cors_violation",
		"bot", "protocol_violation", "data_exposure", "deserialization", "upload_abuse",
		"ldap_injection", "nosql_injection", "brute_force":
		return normalized
	}
	return ""
}
