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
	Total          int64
	Buckets        []Bucket
	Latency        map[string]map[string]float64
	OverallLatency map[string]float64
	Locations      map[string]Location `json:"-"`
}

type Location struct{ Country, Region string }

const VulnerabilityRule = "scanner_vulnerability"

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
	LatencyThreshold           float64
	MetricsURL                 string
	VulnerabilityScan          bool
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
