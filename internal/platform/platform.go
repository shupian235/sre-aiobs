package platform

import (
	"context"
	"time"
)

type Window struct{ Start, End time.Time }
type Bucket struct {
	Dimension, Value string
	Count            int64
}
type Snapshot struct {
	Total           int64
	CloudAlarmCount *int64 `json:"CloudAlarmCount,omitempty"`
	Buckets         []Bucket
	Latency         map[string]map[string]float64
	OverallLatency  map[string]float64
	Locations       map[string]Location `json:"-"`
}

type Location struct{ Country, Region string }

const VulnerabilityRule = "scanner_vulnerability"
const CloudAlarmRule = "aws_cloudwatch_alarm"
const CloudMissingDataRule = "aws_cloudwatch_insufficient_data"

type CloudSummary struct {
	Checked          int64 `json:"checked"`
	OK               int64 `json:"ok"`
	Alarm            int64 `json:"alarm"`
	InsufficientData int64 `json:"insufficient_data"`
}

func SummarizeCloud(checked *int64, rules map[string]int64) *CloudSummary {
	if checked == nil {
		return nil
	}
	alarm, missing := rules[CloudAlarmRule], rules[CloudMissingDataRule]
	return &CloudSummary{Checked: *checked, OK: max(0, *checked-alarm-missing), Alarm: alarm, InsufficientData: missing}
}

type Event struct {
	Rule, IP, Country, Region string
	Target                    string
	Count                     int64
	Score                     int
	Signals                   []string
}
type Analysis struct {
	Summary  string   `json:"summary"`
	Severity string   `json:"severity"`
	Actions  []string `json:"actions"`
}
type Report struct {
	VulnerabilityScan          bool
	NginxSecurity              bool
	RunID                      string
	AWSRegion                  string
	Source, Index, Environment string
	Window                     Window
	Snapshot                   Snapshot
	Events                     []Event
	Analysis                   *Analysis
}
type Job struct {
	ID, CorrelationID, Source, Kind string
	Window                          Window
}
type Source interface {
	Aggregate(context.Context, Window) (Snapshot, error)
}
type Analyzer interface {
	Analyze(context.Context, []Event) (*Analysis, error)
}
