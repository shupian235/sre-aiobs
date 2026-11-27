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
export GRAFANA_URL=""
export VICTORIAMETRICS_WRITE_URL=""
export VICTORIAMETRICS_READ_URL=""
export VICTORIAMETRICS_WRITE_TOKEN=""
if [[ "${TEST_VICTORIAMETRICS:-0}" == 1 ]]; then
  export VICTORIAMETRICS_PORT="${VICTORIAMETRICS_PORT:-18428}"
  export VICTORIAMETRICS_READ_URL="http://127.0.0.1:${VICTORIAMETRICS_PORT}"
  compose+=(-f compose.victoriametrics.yaml -f tests/compose.integration-vm.yaml)
fi
trap '"${compose[@]}" down' EXIT
"${compose[@]}" up -d elastic postgres redis
if [[ "${TEST_VICTORIAMETRICS:-0}" == 1 ]]; then
  "${compose[@]}" up -d victoriametrics
  curl --retry 15 --retry-all-errors --retry-delay 1 -fsS "http://127.0.0.1:${VICTORIAMETRICS_PORT}/health" >/dev/null
fi
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
assert data["requests"] == 7 and data["alerts"] == 8
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
routes_url="$base/api/sources/integration-nginx/routes"
test "$(curl -sS --user "operator:${DASHBOARD_PASSWORD}" -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' --data '[]' "$routes_url")" = 403
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" -X PUT \
  -H "X-Alert-Settings-Password: ${ALERT_SETTINGS_PASSWORD}" -H 'Content-Type: application/json' \
  --data '[{"label":"/orders/:id","pattern":"^/orders/[0-9]+$"}]' "$routes_url"
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$routes_url" | python3 -c '
import json,sys
data=json.load(sys.stdin)
assert data["overridden"] and data["routes"] == [{"label":"/orders/:id","pattern":"^/orders/[0-9]+$"}]
'
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" -X DELETE \
  -H "X-Alert-Settings-Password: ${ALERT_SETTINGS_PASSWORD}" "$routes_url"
curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$routes_url" | python3 -c '
import json,sys
assert not json.load(sys.stdin)["overridden"]
'
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
assert {"nginx_unexpected_method","nginx_legacy_tls","nginx_waf_block","nginx_waf_category"} <= set(data["counts"])
assert any(e["event"]["Target"]=="/admin" and e["event"]["Rule"]=="sensitive_uri" for e in data["events"])
assert all(not e["event"]["Rule"].startswith("backend_") for e in data["events"])
'

curl -fsS --user "operator:${DASHBOARD_PASSWORD}" "$base/api/vulnerabilities?hours=6&source=integration-nginx" | python3 -c '
import json,sys
data=json.load(sys.stdin)
assert data["total"]==1 and data["counts"]=={"scanner_vulnerability":1}
assert data["events"][0]["event"]["Target"]=="CVE-2099-0001"
'

if [[ "${TEST_VICTORIAMETRICS:-0}" == 1 ]]; then
  id="$(sql "SELECT id FROM runs WHERE source='integration-nginx' AND kind='five_minutes' AND window_end='$end'")"
  metrics_link="$(curl -fsS --user "operator:${DASHBOARD_PASSWORD}" -o /dev/null -w '%{redirect_url}' "$base/api/metrics-link/$id")"
  test "${metrics_link%%\?*}" = "http://127.0.0.1:${VICTORIAMETRICS_PORT}/api/v1/query_range"
  test "$(sql "SELECT status FROM notifications WHERE run_id='$id' AND channel='victoriametrics'")" = sent
  for ((i=0; i<20; i++)); do
    if curl -fsS "$metrics_link" | python3 -c '
import json, sys
data = json.load(sys.stdin)
expected_end = int(sys.argv[1])
series = {(row["metric"]["__name__"], row["metric"].get("quantile")): dict(row["values"]) for row in data["data"]["result"]}
logs = series.get(("sre_aiobs_window_logs", None), {})
evaluated = series.get(("sre_aiobs_window_evaluated", None), {})
latency = [series.get(("sre_aiobs_window_latency_seconds", q), {}).get(expected_end) for q in ("0.50", "0.95", "0.99")]
breach = series.get(("sre_aiobs_window_latency_p95_breach", None), {})
if logs.get(expected_end) != "7" or evaluated.get(expected_end) != "1" or breach.get(expected_end) != "1" or any(v is None for v in latency):
    sys.exit(1)
values = list(map(float, latency))
sys.exit(not (0 < values[0] <= values[1] <= values[2]))
' "$window_end"; then exit 0; fi
    sleep 1
  done
  printf 'VictoriaMetrics did not return the expected window samples.\n' >&2
  exit 1
fi
