CREATE TABLE IF NOT EXISTS runs (
 id text PRIMARY KEY, correlation_id text NOT NULL, source text NOT NULL, kind text NOT NULL,
 window_start timestamptz NOT NULL, window_end timestamptz NOT NULL,
 status text NOT NULL, attempts integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source,kind,window_end)
);
CREATE TABLE IF NOT EXISTS snapshots (
 run_id text PRIMARY KEY REFERENCES runs(id), payload jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS risk_events (
 run_id text REFERENCES runs(id), ordinal integer, payload jsonb NOT NULL,
 PRIMARY KEY(run_id,ordinal)
);
CREATE TABLE IF NOT EXISTS ai_analyses (
 run_id text PRIMARY KEY REFERENCES runs(id), payload jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_logs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, run_id text REFERENCES runs(id),
 action text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS notifications (
 run_id text REFERENCES runs(id), channel text, status text NOT NULL,
 attempts integer NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,channel)
);
CREATE INDEX IF NOT EXISTS runs_history_idx ON runs(source,kind,window_end DESC) WHERE status='done';
