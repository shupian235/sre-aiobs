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
ensure_view sre-aiobs-application 'app-logs-*' Applications
ensure_view sre-aiobs-vulnerabilities 'vulnerability-findings-*' Vulnerabilities
