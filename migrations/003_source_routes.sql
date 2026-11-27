CREATE TABLE IF NOT EXISTS source_route_settings (
 source text PRIMARY KEY,
 routes jsonb NOT NULL CHECK (jsonb_typeof(routes) = 'array')
);
