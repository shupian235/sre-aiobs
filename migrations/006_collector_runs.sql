CREATE TABLE IF NOT EXISTS collector_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  action text NOT NULL CHECK (action IN ('check','deploy')),
  status text NOT NULL CHECK (status IN ('running','succeeded','failed','cancelled')),
  host_count integer NOT NULL CHECK (host_count BETWEEN 1 AND 50),
  settings_version bigint NOT NULL,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS collector_one_run ON collector_runs ((true)) WHERE status='running';
CREATE INDEX IF NOT EXISTS collector_runs_started_at ON collector_runs (started_at DESC);
