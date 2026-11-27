CREATE TABLE IF NOT EXISTS alert_settings (
 singleton boolean PRIMARY KEY CHECK (singleton),
 payload jsonb NOT NULL
);
