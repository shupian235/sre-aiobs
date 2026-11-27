#!/usr/bin/env bash
set -euo pipefail

create_view() {
  local id=$1 pattern=$2 name=$3
  curl -fsS --max-time 30 --noproxy '*' --user "elastic:$ELASTIC_PASSWORD" \
    -H 'kbn-xsrf: sre-aiobs-setup' -H 'Content-Type: application/json' \
    -X POST http://kibana:5601/api/data_views/data_view \
    --data-binary "{\"data_view\":{\"id\":\"$id\",\"title\":\"$pattern\",\"name\":\"$name\",\"timeFieldName\":\"@timestamp\",\"allowNoIndex\":true}}" \
    >/dev/null
}

ensure_view() {
  local id=$1 code
  code=$(curl -sS --max-time 30 --noproxy '*' --user "elastic:$ELASTIC_PASSWORD" -o /dev/null -w '%{http_code}' \
    "http://kibana:5601/api/data_views/data_view/$id")
  case "$code" in
    200) ;;
    404) create_view "$@" ;;
    *) printf 'Kibana Data View %s returned HTTP %s\n' "$id" "$code" >&2; return 1 ;;
  esac
}

ensure_view bc589ecf-5117-40ca-b632-cb021652f611 'nginx-access-*' Nginx
ensure_view sre-aiobs-nginx-security 'nginx-security-*' 'Nginx Security'
ensure_view sre-aiobs-application 'app-logs-*' Applications
ensure_view sre-aiobs-vulnerabilities 'vulnerability-findings-*' Vulnerabilities

ensure_dashboard() {
  local id=$1 code result
  code=$(curl -sS --max-time 30 --noproxy '*' --user "elastic:$ELASTIC_PASSWORD" -o /dev/null -w '%{http_code}' \
    "http://kibana:5601/api/saved_objects/dashboard/$id")
  case "$code" in
    200) return ;;
    404) ;;
    *) printf 'Kibana dashboard %s returned HTTP %s\n' "$id" "$code" >&2; return 1 ;;
  esac

  # Only install when the dashboard is missing. Stable object IDs let an
  # interrupted first import replace its own partially imported visualizations.
  result=$(curl -fsS --max-time 90 --noproxy '*' --user "elastic:$ELASTIC_PASSWORD" \
    -H 'kbn-xsrf: sre-aiobs-setup' -F "file=@${BASH_SOURCE[0]%/*}/dashboards/$id.ndjson" \
    'http://kibana:5601/api/saved_objects/_import?overwrite=true')
  if ! grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<<"$result"; then
    printf 'Kibana failed to import dashboard %s: %s\n' "$id" "$result" >&2
    return 1
  fi
  printf 'Installed Kibana dashboard %s\n' "$id"
}

ensure_dashboard sre-aiobs-nginx-access
ensure_dashboard sre-aiobs-nginx-geo
ensure_dashboard sre-aiobs-nginx-security
