#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
check=false
elasticsearch=false
gateway=false
collector=false
elk=false
web_collectors=false
for arg in "$@"; do
  case "$arg" in
    --check) check=true ;;
    --with-web-collectors) web_collectors=true ;;
    --with-elk) elk=true; elasticsearch=true ;;
    --with-elasticsearch) elasticsearch=true ;;
    --with-nginx-collector) collector=true; elasticsearch=true ;;
    --with-ingest-gateway) gateway=true; elasticsearch=true ;;
    *) printf '用法: %s [--check] [--with-elasticsearch] [--with-elk] [--with-ingest-gateway] [--with-nginx-collector] [--with-web-collectors]\n' "$0" >&2; exit 2 ;;
  esac
done

if ! docker compose version >/dev/null 2>&1; then
  printf '请先安装 Docker 和 Docker Compose v2。\n' >&2
  exit 1
fi

if [[ -L .env || ( -e .env && ! -f .env ) ]]; then
  printf '.env 必须是普通文件。\n' >&2
  exit 1
fi
if [[ -f .env ]]; then
  chmod 0600 .env
else
  command -v openssl >/dev/null || { printf '首次启动需要 OpenSSL 生成密码。\n' >&2; exit 1; }
  postgres=$(openssl rand -hex 24)
  redis=$(openssl rand -hex 24)
  dashboard=$(openssl rand -hex 24)
  settings=$(openssl rand -hex 24)
  # The Compose CLI reads this private file and injects values into containers.
  (
    umask 077
    set -o noclobber
    cat >.env <<ENV
COMPOSE_PROJECT_NAME=sre-aiobs
AIOBS_CONFIG=./config.yaml
POSTGRES_PASSWORD=$postgres
REDIS_PASSWORD=$redis
DATABASE_URL=postgres://aiobs:$postgres@postgres:5432/aiobs?sslmode=disable
REDIS_URL=redis://:$redis@redis:6379/0
DASHBOARD_PASSWORD=$dashboard
ALERT_SETTINGS_PASSWORD=$settings
ENV
  )
fi

if ! grep -q '^AIOBS_SETTINGS_KEY=' .env; then
  command -v openssl >/dev/null || { printf '生成配置加密密钥需要 OpenSSL。\n' >&2; exit 1; }
  value=$(openssl rand -base64 32)
  printf 'AIOBS_SETTINGS_KEY=%s\n' "$value" >>.env
fi

secrets=()
if $elasticsearch; then
  secrets+=(ELASTIC_PASSWORD ELASTICSEARCH_READER_PASSWORD ELASTICSEARCH_INGEST_PASSWORD)
fi
if $elk; then
  secrets+=(KIBANA_SYSTEM_PASSWORD KIBANA_SESSION_KEY KIBANA_OBJECTS_KEY KIBANA_REPORTING_KEY)
fi
for key in "${secrets[@]}"; do
  if ! grep -q "^$key=" .env; then
    command -v openssl >/dev/null || { printf '生成部署密钥需要 OpenSSL。\n' >&2; exit 1; }
    value=$(openssl rand -hex 24)
    printf '%s=%s\n' "$key" "$value" >>.env
  fi
done

if [[ ! -e config.yaml ]]; then
  # Contains field mappings and addresses only. Secrets live in .env.
  starter=deploy/single-node/config.starter.yaml
  if $elasticsearch; then starter=deploy/single-node/config.elasticsearch.yaml; fi
  (umask 022; set -o noclobber; cat "$starter" >config.yaml)
fi

compose=(docker compose --env-file .env -f compose.yaml)
if $elasticsearch; then compose+=(-f compose.elasticsearch.yaml); fi
if $gateway; then compose+=(-f compose.elasticsearch-ingress.yaml); fi
if $collector; then compose+=(-f compose.nginx-collector.yaml); fi
if $elk; then
  compose+=(-f compose.elk.yaml)
  if $collector; then compose+=(-f compose.nginx-collector-logstash.yaml); fi
fi
if $web_collectors; then compose+=(-f compose.web-collectors.yaml); fi
"${compose[@]}" config --quiet
if $check; then
  printf '配置通过，运行 %s 启动。\n' "$0"
  exit 0
fi

"${compose[@]}" up -d --build --wait --wait-timeout "${COMPOSE_WAIT_TIMEOUT:-300}"
printf '\n容器状态与端口映射\n'
"${compose[@]}" ps -a
printf '\n默认入口\n'
printf '  运维看板 http://127.0.0.1:8080/（operator，密码见 .env 中的 DASHBOARD_PASSWORD）\n'
if $elk; then
  printf '  Kibana   http://127.0.0.1:%s/（elastic，密码见 .env 中的 ELASTIC_PASSWORD）\n' "${KIBANA_PORT:-5601}"
fi
