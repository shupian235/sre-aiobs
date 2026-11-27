package store

import (
	"context"
	"encoding/json"
	"sre-aiobs/internal/platform"
	"time"
)

type BackendLogs struct {
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
	Events json.RawMessage  `json:"events"`
}

func (s Store) BackendLogs(ctx context.Context, source, kind string, since time.Time, minimum int) (BackendLogs, error) {
	return s.ruleEvents(ctx, source, kind, since, minimum, []string{"backend_critical_log", "backend_error_spike", "backend_warning_spike", "backend_business_error_spike", "backend_exception", "backend_dependency_failure"})
}

func (s Store) Vulnerabilities(ctx context.Context, source, kind string, since time.Time, minimum int) (BackendLogs, error) {
	return s.ruleEvents(ctx, source, kind, since, minimum, []string{platform.VulnerabilityRule})
}

func (s Store) NginxRiskRoutes(ctx context.Context, source, kind string, since time.Time, minimum int) (BackendLogs, error) {
	return s.ruleEvents(ctx, source, kind, since, minimum, platform.NginxRules())
}

func (s Store) ruleEvents(ctx context.Context, source, kind string, since time.Time, minimum int, rules []string) (BackendLogs, error) {
	var result BackendLogs
	err := s.DB.QueryRow(ctx, `
 WITH matched AS MATERIALIZED (
  SELECT runs.id,runs.correlation_id,runs.source,runs.window_end AS at,risk_events.ordinal,
   risk_events.payload AS event,risk_events.payload->>'Rule' AS rule,(risk_events.payload->>'Score')::integer AS score
  FROM runs JOIN risk_events ON risk_events.run_id=runs.id
  WHERE runs.kind=$2 AND runs.window_end>$3 AND runs.window_end<=now() AND ($1='' OR runs.source=$1)
   AND risk_events.payload->>'Rule' = ANY($5::text[])
   AND (risk_events.payload->>'Score')::integer >= $4
 ), recent AS (
  SELECT id,correlation_id,source,at,ordinal,event,score FROM matched
  ORDER BY at DESC,score DESC,id,ordinal LIMIT 100
 )
 SELECT (SELECT count(*) FROM matched),
  COALESCE((SELECT jsonb_object_agg(rule,count) FROM (SELECT rule,count(*) AS count FROM matched GROUP BY rule) grouped),'{}'),
  COALESCE((SELECT jsonb_agg(to_jsonb(detail) ORDER BY at DESC,score DESC,id,ordinal) FROM (
   SELECT recent.*,ai_analyses.payload AS analysis FROM recent LEFT JOIN ai_analyses ON ai_analyses.run_id=recent.id
  ) detail),'[]')
 `, source, kind, since, minimum, rules).Scan(&result.Total, &result.Counts, &result.Events)
	return result, err
}
