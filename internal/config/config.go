package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Source struct {
	Name           string
	Kind           string
	URL            string
	Index          string
	Environment    string
	Selector       string
	CredentialEnv  string `yaml:"credential_env"`
	Tenant         string
	Fields         map[string]string
	Hosts          []string
	Routes         []Route
	AllowedMethods []string `yaml:"allowed_methods"`
}

type Route struct {
	Label   string
	Pattern string
}

type Rules struct {
	IP                 int64
	URI                int64
	Errors             int64
	BackendErrors      int64 `yaml:"backend_errors"`
	DependencyErrors   int64 `yaml:"dependency_errors"`
	Latency            float64
	BaselineMultiplier float64 `yaml:"baseline_multiplier"`
	Sensitive          []string
	UserAgents         []string `yaml:"user_agents"`
	NginxBurst         int64    `yaml:"nginx_burst"`
}

type AI struct {
	Provider      string
	Model         string
	DailyRequests int `yaml:"daily_requests"`
	Timeout       time.Duration
}

type Config struct {
	Listen       string
	Sources      []Source
	Rules        Rules
	AI           AI
	TelegramChat string   `yaml:"telegram_chat"`
	EmailFrom    string   `yaml:"email_from"`
	EmailTo      []string `yaml:"email_to"`
	GeoDB        string   `yaml:"geo_db"`
}

var methodName = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,31}$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
var elasticField = regexp.MustCompile(`^[a-zA-Z0-9_@.-]+$`)
var lokiField = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var indexPattern = regexp.MustCompile(`^[a-zA-Z0-9_.*,-]+$`)

func Load(path string) (Config, error) {
	c := Config{Listen: ":8080", Rules: Rules{IP: 1000, URI: 2000, Errors: 100, BackendErrors: 20, NginxBurst: 20, DependencyErrors: 5, Latency: 1, BaselineMultiplier: 3}, AI: AI{Timeout: 15 * time.Second}}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("expected one YAML document")
	}
	if len(c.Sources) == 0 || c.Rules.IP < 1 || c.Rules.URI < 1 || c.Rules.Errors < 1 || c.Rules.BackendErrors < 1 || c.Rules.DependencyErrors < 1 || c.Rules.NginxBurst < 1 || c.Rules.Latency <= 0 || c.Rules.BaselineMultiplier <= 1 {
		return c, fmt.Errorf("invalid sources or thresholds")
	}
	if c.AI.Provider != "" && (c.AI.Provider != "openai" && c.AI.Provider != "claude" || c.AI.Model == "" || c.AI.DailyRequests < 1 || c.AI.Timeout <= 0) {
		return c, fmt.Errorf("invalid AI settings")
	}
	for _, rule := range append(append([]string{}, c.Rules.Sensitive...), c.Rules.UserAgents...) {
		if _, err = regexp.Compile(rule); err != nil {
			return c, fmt.Errorf("invalid rule expression: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		u, e := url.Parse(s.URL)
		if !identifier.MatchString(s.Name) || seen[s.Name] || s.Environment == "" || e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("invalid source %q", s.Name)
		}
		if s.CredentialEnv != "" && os.Getenv(s.CredentialEnv) == "" {
			return c, fmt.Errorf("missing credential for %s", s.Name)
		}
		if s.Kind == "elasticsearch" {
			if !indexPattern.MatchString(s.Index) || !elasticField.MatchString(s.Fields["timestamp"]) {
				return c, fmt.Errorf("invalid index or timestamp field for %s", s.Name)
			}
		} else if s.Kind != "loki" || s.Selector == "" {
			return c, fmt.Errorf("invalid source kind or Loki selector for %s", s.Name)
		}
		for _, field := range s.Fields {
			if (s.Kind == "elasticsearch" && !elasticField.MatchString(field)) || (s.Kind == "loki" && !lokiField.MatchString(field)) {
				return c, fmt.Errorf("invalid field for %s", s.Name)
			}
		}
		for _, method := range s.AllowedMethods {
			if !methodName.MatchString(method) {
				return c, fmt.Errorf("invalid allowed method for %s", s.Name)
			}
		}
		for _, route := range s.Routes {
			if label := strings.TrimSpace(route.Label); label == "" || len(label) > 100 || strings.ContainsAny(label, "\r\n") {
				return c, fmt.Errorf("invalid route label for %s", s.Name)
			}
			if route.Pattern == "" {
				return c, fmt.Errorf("empty route pattern for %s", s.Name)
			}
			if _, err := regexp.Compile(route.Pattern); err != nil {
				return c, fmt.Errorf("invalid route pattern for %s: %w", s.Name, err)
			}
		}
		seen[s.Name] = true
	}
	return c, nil
}
