#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export POSTGRES_PASSWORD="$(openssl rand -hex 16)"
export SLACK_WEBHOOK_URL=""
export ALERT_SETTINGS_PASSWORD="$(openssl rand -hex 16)"
export DASHBOARD_PASSWORD="$(openssl rand -hex 16)"
export REDIS_PASSWORD="$(openssl rand -hex 16)"
export DATABASE_URL="postgres://aiobs:${POSTGRES_PASSWORD}@postgres:5432/aiobs?sslmode=disable"
export REDIS_URL="redis://:${REDIS_PASSWORD}@redis:6379/0"
export AIOBS_CONFIG=./tests/config.yaml
export NO_PROXY="localhost,127.0.0.1" no_proxy="localhost,127.0.0.1"
export TEST_ES_PORT="${TEST_ES_PORT:-19200}"
export AIOBS_PORT="${AIOBS_PORT:-18080}"
export COMPOSE_PROJECT_NAME="sre-aiobs-it-$(openssl rand -hex 8)"
compose=(docker compose -p "$COMPOSE_PROJECT_NAME" -f compose.yaml -f tests/compose.integration.yaml)
trap '"${compose[@]}" down' EXIT
"${compose[@]}" up -d elastic postgres redis
"${compose[@]}" build app migrate
for ((i=0; i<90; i++)); do
  if curl -fsS "http://127.0.0.1:${TEST_ES_PORT}/_cluster/health" >/dev/null 2>&1; then break; fi
  sleep 2
done
window_end="$(( $(date -u +%s) / 300 * 300 - 300 ))"
stamp="$(date -u -d "@$((window_end-10))" +%FT%TZ)"
end="$(date -u -d "@$window_end" +%FT%TZ)"
python3 - "$stamp" <<'PY' | curl -fsS -H 'Content-Type: application/x-ndjson' -X POST "http://127.0.0.1:${TEST_ES_PORT}/_bulk?refresh=true" --data-binary @- | python3 -c 'import json,sys; assert not json.load(sys.stdin)["errors"]'
import itertools,json,sys
with open("testdata/nginx.jsonl") as nginx, open("testdata/backend.jsonl") as backend, open("testdata/vulnerability.jsonl") as scan:
    lines=list(itertools.chain(nginx,backend,scan))
for line in lines:
    event=json.loads(line)
    event["@timestamp"]=sys.argv[1]
    print(json.dumps({"index":{"_index":"nginx-test"}}))
    print(json.dumps(event))
PY
"${compose[@]}" run --rm migrate
"${compose[@]}" run --rm --no-deps app -config /etc/sre-aiobs/config.yaml -backfill "$end"
"${compose[@]}" run --rm --no-deps app -config /etc/sre-aiobs/config.yaml -backfill "$end"
sql() {
  "${compose[@]}" exec -T postgres psql -U aiobs -d aiobs -Atqc "$1"
}
test "$(sql "SELECT count(*) FROM runs WHERE source='integration-nginx' AND kind='minute' AND status='done' AND attempts=1")" = 1
test "$(sql "SELECT payload->>'Total' FROM snapshots")" = 7
test "$(sql "SELECT count(*) FROM risk_events WHERE payload->>'Rule'='correlated' AND payload->>'IP'='192.0.2.20'")" = 1
test "$(sql "SELECT count(*) FROM snapshots WHERE payload::text LIKE '%fixture-only%'")" = 0
"${compose[@]}" run --rm --no-deps app -config /etc/sre-aiobs/config.yaml -backfill "$end" -kind five_minutes
"${compose[@]}" up -d --no-deps app
curl --retry 15 --retry-all-errors --retry-delay 1 -fsS "http://127.0.0.1:${AIOBS_PORT}/readyz"
curl -fsS "http://127.0.0.1:${AIOBS_PORT}/healthz"
curl -fsS "http://127.0.0.1:${AIOBS_PORT}/metrics" | grep '^sre_aiobs_runs_total '

test "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${AIOBS_PORT}/api/dashboard")" = 401
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "http://127.0.0.1:${AIOBS_PORT}/" | grep '安全概览' >/dev/null
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "http://127.0.0.1:${AIOBS_PORT}/api/dashboard?hours=6&score=80&source=integration-nginx" | python3 -c '
import json, sys
data = json.load(sys.stdin)
assert data["kind"] == "five_minutes"
assert data["requests"] == 7 and data["alerts"] == 5
assert any(e["event"]["IP"] == "192.0.2.20" for e in data["events"])
assert "integration-nginx" in data["sources"]
assert {"backend_exception","backend_critical_log","backend_dependency_failure"} <= {e["event"]["Rule"] for e in data["events"]}
'

settings_url="http://127.0.0.1:${AIOBS_PORT}/api/alert-settings"
settings_payload='{"minimum_score":92,"cooldown_minutes":27,"slack":false,"telegram":true,"email":false}'
test "$(curl -sS --user "operator:${DASHBOARD_PASSWORD}" -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' --data "$settings_payload" "$settings_url")" = 403
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" -X PUT -H "X-Alert-Settings-Password: ${ALERT_SETTINGS_PASSWORD}" -H 'Content-Type: application/json' --data "$settings_payload" "$settings_url"
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$settings_url" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["minimum_score"] == 92 and d["cooldown_minutes"] == 27 and not d["slack"] and d["editable"]'
test "$(sql "SELECT count(*) FROM audit_logs WHERE action='alert_settings_updated'")" = 1

curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "http://127.0.0.1:${AIOBS_PORT}/api/backend-logs?hours=6&score=80&source=integration-nginx" | python3 -c '
import json, sys
data = json.load(sys.stdin)
expected = {"backend_critical_log": 1, "backend_exception": 1, "backend_dependency_failure": 1}
assert data["total"] == 3 and data["counts"] == expected
assert {e["event"]["Rule"] for e in data["events"]} == set(expected)
assert all(e["source"] == "integration-nginx" for e in data["events"])
'

base="http://127.0.0.1:${AIOBS_PORT}"
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$base/api/sources" | python3 -c '
import json,sys
assert json.load(sys.stdin)==[{"name":"integration-nginx","kind":"elasticsearch","environment":"test","backend":True,"vulnerabilities":True}]
'
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" -X POST -H 'X-SRE-AIObs-Check: 1' "$base/api/sources/integration-nginx/check" | python3 -c '
import json,sys
assert json.load(sys.stdin)["status"]=="ok"
'
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$base/api/nginx-risk-routes?hours=6&score=65&source=integration-nginx" | python3 -c '
import json,sys
data=json.load(sys.stdin)
assert data["total"]>=1
assert all(e["event"]["Target"]=="/admin" and e["event"]["Rule"]=="sensitive_uri" for e in data["events"])
'

curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$base/api/vulnerabilities?hours=6&source=integration-nginx" | python3 -c '
import json,sys
data=json.load(sys.stdin)
assert data["total"]==1 and data["counts"]=={"scanner_vulnerability":1}
assert data["events"][0]["event"]["Target"]=="CVE-2099-0001"
'
