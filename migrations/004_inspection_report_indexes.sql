CREATE INDEX IF NOT EXISTS runs_kind_window_idx ON runs(kind, window_end DESC);
CREATE INDEX IF NOT EXISTS risk_events_run_rule_score_idx
 ON risk_events(run_id, (payload->>'Rule'), ((payload->>'Score')::integer));
