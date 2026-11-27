package store

import (
	"context"
	"encoding/json"
	"time"
)

type Dashboard struct {
	Runs     int64           `json:"runs"`
	Failed   int64           `json:"failed"`
	Requests int64           `json:"requests"`
	Alerts   int64           `json:"alerts"`
	Trend    json.RawMessage `json:"trend"`
	Rules    json.RawMessage `json:"rules"`
	Events   json.RawMessage `json:"events"`
	Tasks    json.RawMessage `json:"tasks"`
}

func (s Store) Dashboard(ctx context.Context, source, kind string, since time.Time, minimum int) (Dashboard, error) {
	var data Dashboard
	err := s.DB.QueryRow(ctx, `
 WITH selected AS MATERIALIZED (
  SELECT id,correlation_id,source,window_end,status,attempts FROM runs
  WHERE window_end > $3 AND window_end <= now() AND kind=$2 AND ($1='' OR source=$1)
 ), volumes AS (
  SELECT window_end AS at, sum((payload->>'Total')::bigint)::bigint AS count
  FROM selected JOIN snapshots ON snapshots.run_id=selected.id GROUP BY window_end
 ), risks AS (
  SELECT selected.id, correlation_id, source, window_end AS at, r.ordinal,
   r.payload AS event, (r.payload->>'Score')::integer AS score
  FROM selected JOIN risk_events r ON r.run_id=selected.id
 )
 SELECT (SELECT count(*) FROM selected),
  (SELECT count(*) FROM selected WHERE status='failed'),
  COALESCE((SELECT sum(count)::bigint FROM volumes),0),
  (SELECT count(*) FROM risks WHERE score>=80),
  COALESCE((SELECT jsonb_agg(to_jsonb(volumes) ORDER BY at) FROM volumes),'[]'),
  COALESCE((SELECT jsonb_agg(to_jsonb(grouped) ORDER BY count DESC, rule) FROM (
   SELECT event->>'Rule' AS rule,count(*) AS count FROM risks WHERE score >= $4
   GROUP BY 1 ORDER BY count DESC,rule LIMIT 6
  ) grouped),'[]'),
  COALESCE((SELECT jsonb_agg(to_jsonb(recent) ORDER BY at DESC,score DESC,id,ordinal) FROM (
   SELECT risks.*, a.payload AS analysis FROM risks LEFT JOIN ai_analyses a ON a.run_id=risks.id
   WHERE score >= $4 ORDER BY at DESC,score DESC,id,ordinal LIMIT 100
  ) recent),'[]'),
  COALESCE((SELECT jsonb_agg(to_jsonb(tasks) ORDER BY window_end DESC,id) FROM (
   SELECT * FROM selected ORDER BY window_end DESC,id LIMIT 100
  ) tasks),'[]')
 `, source, kind, since, minimum).Scan(&data.Runs, &data.Failed, &data.Requests, &data.Alerts, &data.Trend, &data.Rules, &data.Events, &data.Tasks)
	return data, err
}

func (s Store) DashboardSources(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT source FROM runs WHERE window_end > now() - interval '8 days' ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]string, 0)
	for rows.Next() {
		var source string
		if err = rows.Scan(&source); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}
