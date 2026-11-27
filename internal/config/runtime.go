package config

import (
	"errors"
	"math"
	"strings"
)

type AWSCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

type ManagedAWS struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Region      string `json:"region"`
	AccountID   string `json:"account_id"`
	AlarmPrefix string `json:"alarm_prefix"`
}

type ManagedRules struct {
	IP                 int64   `json:"ip"`
	URI                int64   `json:"uri"`
	Errors             int64   `json:"errors"`
	Latency            float64 `json:"latency"`
	BaselineMultiplier float64 `json:"baseline_multiplier"`
	BackendErrors      *int64  `json:"backend_errors,omitempty"`
	BackendWarnings    *int64  `json:"backend_warnings,omitempty"`
	BusinessErrors     *int64  `json:"business_errors,omitempty"`
	DependencyErrors   *int64  `json:"dependency_errors,omitempty"`
	NginxBurst         *int64  `json:"nginx_burst,omitempty"`
}

type ManagedAI struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	DailyRequests int    `json:"daily_requests"`
}

type RuntimeSettings struct {
	AWS         []ManagedAWS    `json:"aws"`
	Credentials *AWSCredentials `json:"credentials,omitempty"`
	Rules       *ManagedRules   `json:"rules,omitempty"`
	AI          *ManagedAI      `json:"ai,omitempty"`
}

func (s RuntimeSettings) Validate() error {
	if len(s.AWS) > 20 {
		return errors.New("too many AWS sources")
	}
	seen := map[string]bool{}
	for _, src := range s.AWS {
		if len(src.Name) > 80 || !identifier.MatchString(src.Name) || seen[src.Name] ||
			len(src.Environment) > 80 || strings.TrimSpace(src.Environment) == "" ||
			len(src.Region) > 40 || !awsRegion.MatchString(src.Region) ||
			(src.AccountID != "" && !awsAccount.MatchString(src.AccountID)) ||
			len(src.AlarmPrefix) > 255 || strings.ContainsAny(src.AlarmPrefix, "\r\n") {
			return errors.New("invalid or duplicate AWS source")
		}
		seen[src.Name] = true
	}
	if c := s.Credentials; c != nil {
		if len(c.AccessKeyID) < 16 || len(c.AccessKeyID) > 128 || len(c.SecretAccessKey) < 16 || len(c.SecretAccessKey) > 128 ||
			len(c.SessionToken) > 4096 || strings.ContainsAny(c.AccessKeyID+c.SecretAccessKey+c.SessionToken, " \t\r\n") {
			return errors.New("invalid or incomplete AWS credentials")
		}
	}
	if r := s.Rules; r != nil {
		if r.IP < 1 || r.URI < 1 || r.Errors < 1 || r.Latency <= 0 || r.BaselineMultiplier <= 1 ||
			below(r.BackendErrors, 1) || below(r.BackendWarnings, 0) || below(r.BusinessErrors, 0) || below(r.DependencyErrors, 1) || below(r.NginxBurst, 1) ||
			math.IsNaN(r.Latency) || math.IsInf(r.Latency, 0) || math.IsNaN(r.BaselineMultiplier) || math.IsInf(r.BaselineMultiplier, 0) {
			return errors.New("invalid inspection thresholds")
		}
	}
	if a := s.AI; a != nil && (a.Provider != "" &&
		((a.Provider != "openai" && a.Provider != "claude") || strings.TrimSpace(a.Model) == "" ||
			len(a.Model) > 120 || a.DailyRequests < 1 || a.DailyRequests > 100000)) {
		return errors.New("invalid AI settings")
	}
	return nil
}

func below(value *int64, minimum int64) bool { return value != nil && *value < minimum }

func (s RuntimeSettings) Apply(base Config) (Config, error) {
	sources := append([]Source(nil), base.Sources...)
	for _, src := range s.AWS {
		for _, existing := range sources {
			if existing.Name == src.Name {
				return base, errors.New("Web AWS source name conflicts with a deployment source")
			}
		}
		sources = append(sources, Source{Name: src.Name, Kind: "aws", Environment: src.Environment, Region: src.Region,
			AWSAccountID: src.AccountID, AlarmPrefix: src.AlarmPrefix, AWSCredentials: s.Credentials})
	}
	base.Sources = sources
	if r := s.Rules; r != nil {
		base.Rules.IP, base.Rules.URI, base.Rules.Errors = r.IP, r.URI, r.Errors
		base.Rules.Latency, base.Rules.BaselineMultiplier = r.Latency, r.BaselineMultiplier
		for _, threshold := range []struct{ target, update *int64 }{
			{&base.Rules.BackendErrors, r.BackendErrors}, {&base.Rules.BackendWarnings, r.BackendWarnings},
			{&base.Rules.BusinessErrors, r.BusinessErrors}, {&base.Rules.DependencyErrors, r.DependencyErrors},
			{&base.Rules.NginxBurst, r.NginxBurst},
		} {
			if threshold.update != nil {
				*threshold.target = *threshold.update
			}
		}
	}
	if a := s.AI; a != nil {
		base.AI.Provider, base.AI.Model, base.AI.DailyRequests = a.Provider, a.Model, a.DailyRequests
	}
	return base, nil
}
