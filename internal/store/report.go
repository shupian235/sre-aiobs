package store

import (
	"context"
	"time"

	"sre-aiobs/internal/platform"
)

type RunReport struct {
	ID            string             `json:"run_id"`
	CorrelationID string             `json:"correlation_id"`
	Source        string             `json:"source"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`
	Start         time.Time          `json:"window_start"`
	End           time.Time          `json:"window_end"`
	Updated       time.Time          `json:"updated_at"`
	Attempts      int                `json:"attempts"`
	Total         *int64             `json:"log_count"`
	Latency       map[string]float64 `json:"latency_seconds,omitempty"`
	ByRule        map[string]int64   `json:"events_by_rule"`
	Delivery      map[string]string  `json:"delivery_status"`
	Analysis      *platform.Analysis `json:"analysis,omitempty"`
}

func (s Store) RunReport(ctx context.Context, id string) (RunReport, error) {
	var report RunReport
	err := s.DB.QueryRow(ctx, `
SELECT r.id,r.correlation_id,r.source,r.kind,r.status,r.window_start,r.window_end,r.updated_at,r.attempts,
 (snapshots.payload->>'Total')::bigint, snapshots.payload->'OverallLatency',
 COALESCE((SELECT jsonb_object_agg(rule,total) FROM (
  SELECT payload->>'Rule' AS rule,count(*) AS total FROM risk_events WHERE run_id=r.id GROUP BY 1
 ) grouped),'{}'::jsonb),
 COALESCE((SELECT jsonb_object_agg(channel,status) FROM notifications WHERE run_id=r.id),'{}'::jsonb),ai_analyses.payload
FROM runs r LEFT JOIN snapshots ON snapshots.run_id=r.id LEFT JOIN ai_analyses ON ai_analyses.run_id=r.id
WHERE r.id=$1`, id).Scan(&report.ID, &report.CorrelationID, &report.Source, &report.Kind, &report.Status,
		&report.Start, &report.End, &report.Updated, &report.Attempts, &report.Total, &report.Latency, &report.ByRule,
		&report.Delivery, &report.Analysis)
	return report, err
}
