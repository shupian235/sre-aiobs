# 配置与部署

首次部署可先看 [单机快速部署](quickstart.md)，看板和链路见 [功能预览](preview/README.md)。

## 日志源

每条 `sources` 配置的 `name` 必须唯一，独立标识逻辑数据源。相同集群可配置多个来源，通过 `index` 或 Loki `selector` 隔离不同环境。一个 Elasticsearch 来源可指定索引别名、通配符或逗号分隔表达式。相同字段在表达式匹配的索引中应具备兼容映射。时间使用日期字段，字符串维度使用可聚合的 keyword 或 IP 字段，响应时延使用秒为单位的数值。Loki 读取现有 JSON 日志字段，并受现有查询超时和序列上限约束。可按业务拆分高基数数据源以限制查询资源。

由 Elasticsearch 管理员在批准后创建 API key，角色仅授权指定索引的 `read`，例如：

```json
{
  "name": "sre-aiobs",
  "role_descriptors": {
    "nginx_reader": {
      "cluster": [],
      "indices": [{"names": ["nginx-access-*"], "privileges": ["read"]}]
    }
  }
}
```

将编码后的 key 注入 `ELASTICSEARCH_API_KEY` 或对应来源的 `credential_env`。Loki 可使用 Bearer Token 与 `tenant`。生产使用 TLS，禁止给运行账号授予写日志、管理集群或封禁 IP 的权限。

## 规则、调度和数据

`rules.ip`、`rules.uri`、`rules.errors` 是每窗口请求数下限。动态阈值取配置下限与最近最多 30 个同源同粒度窗口平均值乘 `baseline_multiplier` 的较大值。`rules.latency` 以秒计，针对 P95 触发。以 Go 正则定义 `sensitive`、`user_agents`，各来源 `hosts` 为大小写不敏感的精确列表。配置文件以原子替换方式发布，服务在调度循环开始时读取，验证失败则保留最后有效配置。监听地址及 GeoIP 数据库路径在启动时读取。地域识别需挂载 GeoLite2 City 或兼容 MMDB。

UTC 每分钟、五分钟、小时、日窗口延迟 15 秒后执行。任务使用 Redis 租约和 PostgreSQL 唯一键，最多自动尝试八次。历史窗口在最早已有窗口之后、最近 13 个窗口内自动发现，每次调度最多处理两个。更早的窗口用 `-backfill 2026-11-26T10:00:00Z -kind hour` 补偿。发送后进程崩溃存在重复外部通知的可能，持久通知状态和 Telegram 冷却可降低重复概率。手工补偿已完成窗口会沿用幂等行为。

数据库保留聚合值、哈希后的维度标签、风险事件与审计记录。邮件以规则序号和路径哈希展示敏感 URI，延迟表以接口哈希展示 P50、P95 和 P99。IP 及地理数据按访问控制和组织保留政策管理。运行账号只需业务表的相应 DML 和审计序列权限，使用单独的迁移凭据执行 DDL。部署方配置数据库备份、保留周期和 Redis 持久化与认证。

## AI 与通知

AI 默认关闭。设置 `ai.provider` 为 `openai` 或 `claude` 并填写 `ai.model`。通过 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY` 注入凭据。AI 输入最多 20 条规则筛选事件，仅包含固定规则标识、计数、评分、规范化 IP 及地理代码。两种 Provider 的输出统一验证。Redis 原子计数限制每分钟十次及每天 `daily_requests` 次，重试占用配额。每次输出最多 600 tokens，并按模型单价核算预算。规则与通知可在分析失败时继续运行。AI 动作只写入报告供人工审阅。

填写 `telegram_chat`，注入 `TELEGRAM_BOT_TOKEN`。分钟任务通知风险分数不低于 80 的事件，同一来源、规则、IP 默认冷却 15 分钟，评分与冷却时间可在看板内调整。填写 `email_from` 和 `email_to` 启用小时及日报。AWS 凭据使用工作负载角色或环境 Secret，设置 `AWS_REGION`。SES 发件身份须验证，沙箱下收件身份也须验证，IAM 按发件身份限制 `ses:SendEmail`。

## 部署

### 单机部署包

将项目改动提交后，在 Git 工作区根目录生成部署包。脚本只打包当前提交的文件：

```sh
./deploy/single-node/package.sh
```

将 `dist/sre-aiobs-single-node.tar.gz` 传到目标机。目标机需安装 Docker、Compose v2 和 OpenSSL，并能拉取基础镜像和 Go 模块。部署包包含构建源码、Compose 清单、数据库迁移、Grafana 配置及预览。

```sh
tar -xzf sre-aiobs-single-node.tar.gz
cd sre-aiobs
./deploy/single-node/start.sh
```

首次运行会生成 `.env` 和空数据源配置，启动 Go 服务、PostgreSQL、Redis、VictoriaMetrics 和 Grafana。再次运行会复用配置、密码和数据卷。若沿用已有 `.env`，补齐 `GRAFANA_ADMIN_PASSWORD` 后再运行。查看端口映射可用脚本输出，默认看板为 `http://127.0.0.1:8080/`，Grafana 为 `http://127.0.0.1:3001/`，登录密码在目标机的 `.env`。可以用 `./deploy/single-node/start.sh --check` 只校验 Compose 配置。

接入真实日志时，参考 `config.example.yaml` 将数据源加入 `config.yaml`，然后再次运行启动脚本，使容器重新读取配置和环境变量。新接入的凭据写入 `.env`。给远程查看者配置内网 HTTPS 入口，并将 `GRAFANA_URL` 和 `VICTORIAMETRICS_READ_URL` 设置为对应的可访问地址。修改 Grafana 密码时，已有数据卷中的管理员密码需在 Grafana 管理界面更新。

### Docker Compose

在能访问日志源的机器安装 Docker 和 Compose v2，然后克隆仓库。

```sh
git clone https://github.com/shupian235/sre-aiobs.git
cd sre-aiobs
cp config.example.yaml config.yaml
```

编辑 `config.yaml`。仅保留要接入的 Elasticsearch 或 Loki 数据源，将地址、索引或标签选择器、字段名替换为实际值。保留应用监听地址 `:8080`，或同步修改 Compose 的容器端口映射。

首次部署时创建 `.env`，将下列占位值替换为强随机值。同一个 PostgreSQL 或 Redis 密码要在各自密码变量和连接串中保持一致。如果密码含 URL 保留字符，连接串中的密码需做百分号编码。

```dotenv
POSTGRES_PASSWORD=replace_with_postgres_password
REDIS_PASSWORD=replace_with_redis_password
DATABASE_URL=postgres://aiobs:replace_with_postgres_password@postgres:5432/aiobs?sslmode=disable
REDIS_URL=redis://:replace_with_redis_password@redis:6379/0
DASHBOARD_PASSWORD=replace_with_dashboard_password
ALERT_SETTINGS_PASSWORD=replace_with_settings_password
AIOBS_CONFIG=./config.yaml
```

按需在 `.env` 中配置与数据源匹配的 `ELASTICSEARCH_API_KEY` 或 `LOKI_TOKEN`，以及 Slack、Telegram、AI 或 SES 凭据。已有环境应继续使用原数据库密码。`config.yaml` 和 `.env` 已列入 Git 忽略规则。

```sh
docker compose config --quiet
docker compose up -d --build
docker compose ps -a
curl -fsS http://127.0.0.1:8080/readyz
```

迁移服务成功退出后，应用启动。用浏览器打开 `http://127.0.0.1:8080/`，用户填 `operator`，密码填 `DASHBOARD_PASSWORD` 的值。Compose 将端口绑定到宿主机回环地址。生产环境通过内网 HTTPS 入口访问，并根据实际存储配置验证 PostgreSQL TLS 和 Redis TLS。首次登录后在「日志数据源」点击「检查」。

### Kubernetes

准备集群可访问的 PostgreSQL 和 Redis。构建镜像并推送到集群可拉取的镜像仓库，再把清单中 Job 和 Deployment 的镜像替换为同一个实际版本。

```sh
docker build -t registry.example.net/sre-aiobs:1.0.0 .
docker push registry.example.net/sre-aiobs:1.0.0
```

在目标命名空间准备好 `config.yaml` 和两个部署用环境文件。运行环境文件包含 `DATABASE_URL`、`REDIS_URL`、`DASHBOARD_PASSWORD` 及启用功能所需的凭据。迁移环境文件包含有 DDL 权限的 `DATABASE_URL`。两者指向同一个数据库。

```sh
kubectl create configmap sre-aiobs-config --from-file=config.yaml
kubectl create secret generic sre-aiobs-env --from-env-file=/secure/path/runtime.env
kubectl create secret generic sre-aiobs-migration-env --from-env-file=/secure/path/migration.env
kubectl apply -f deploy/kubernetes/deployment.yaml
kubectl wait --for=condition=complete job/sre-aiobs-migrate-002 --timeout=180s
kubectl rollout status deployment/sre-aiobs --timeout=180s
kubectl port-forward service/sre-aiobs 8080:8080
```

以上命令使用当前 Kubernetes 命名空间。升级迁移时，为新版本的 Job 使用新的名称，并等待迁移完成。挂载 GeoIP 数据库时更新卷配置。团队访问使用内部 HTTPS 入口，监控配置使用 `/metrics`。应用通过 `/healthz` 和 `/readyz` 暴露健康与就绪状态。

## 应用后端日志探测

### 判断是否需要采集 Agent

| 当前情况 | 接入方式 |
| --- | --- |
| Grafana Explore 已能从 Loki 查到该应用日志，或 Elasticsearch 已有对应索引 | 在 `sre-aiobs` 配置源地址、选择器或索引、字段映射。无需新增 Agent。 |
| 应用只写 stdout 或本机文件，存储端查不到 | 优先让已有日志采集器覆盖该应用。只有现有采集器无法覆盖时才部署采集 Agent。 |
| 应用节点在内网，Loki 已有日志，但 `sre-aiobs` 无法访问 | 将 `sre-aiobs` 部署到同一内网或使用已有私网连接，让其能访问 Loki 查询网关。 |

在看板「日志数据源」点击「检查」。成功且计数大于零表示最近一分钟有日志匹配，成功但计数为零可先用 Grafana Explore 拉长时间范围，核对租户和选择器。探测规则还需要日志包含已映射的级别或故障分类字段。接入后在「风险事件」按数据源查看已保存的应用运行事件。


在 `sources` 中为应用日志添加独立来源。建议每个业务服务使用独立名称、索引或 Loki 选择器，便于在看板中精确筛选。可与已有 Nginx 来源并行使用。

```yaml
sources:
  - name: orders-backend
    kind: elasticsearch
    environment: production
    url: https://elasticsearch.example.net:9200
    credential_env: ELASTICSEARCH_API_KEY
    index: orders-app-*
    fields:
      timestamp: "@timestamp"
      level: log.level.keyword
      exception: error.type.keyword
      dependency: dependency.failure.keyword
rules:
  backend_errors: 20
  dependency_errors: 5
```

Elasticsearch 使用可聚合字段。Loki 可将相同逻辑字段映射到现有 JSON 标签，例如 `fields: {level: level, exception: exception_type, dependency: dependency_failure}`。依赖字段只在调用失败时写入分类，例如 `payments_timeout`。异常字段映射到未处理异常类型；如果日志包含已处理异常，请选择只覆盖未处理异常的日志流或字段。

严重级别如 `fatal`、`panic` 和 `critical` 出现即产生事件。普通错误和下游依赖故障按各自分类计数，同时应用固定下限和历史基线倍率。异常类型出现即记录异常事件。应用事件与 Nginx 事件一起进入看板、Telegram、Slack 和邮件报告。异常和依赖标识以哈希展示，消息正文、堆栈和凭据不进入聚合或通知。可用 [模拟日志](../testdata/backend.jsonl) 核对字段映射。

### 漏洞发现与定期邮件

漏洞检测面板接入可信漏洞扫描器（如已有的镜像或依赖扫描流水线）的结构化结果。每条发现应单独记录，并提供一个 CVE 字段。为扫描结果在 Loki 或 Elasticsearch 建立独立的 `sources` 项，配置 `fields.vulnerability`。Loki 示例为 `fields: {vulnerability: vulnerability_id}`，Elasticsearch 示例为 `vulnerability: vulnerability.id.keyword`，并同时映射 `timestamp`。

探测任务按窗口聚合 CVE。同一窗口中同一 CVE 合并计数。看板和 `GET /api/vulnerabilities` 展示报告的 CVE、扫描记录数和来源。看板通用风险筛选使用固定的分类优先级 80，漏洞真实严重度以原扫描报告为准。

配置现有 AWS SES 的 `email_from`、`email_to` 和 `AWS_REGION`，再在看板「告警设置」启用「邮件小时报告与日报」。UTC 每个整点和每日零点为各扫描数据源发送报告，即使窗口内没有 CVE。邮件仍使用现有投递重试和审计状态。

## Nginx 安全巡检

看板「Nginx 安全巡检」和 `GET /api/nginx-risk-routes` 按数据源、时间和评分查看巡检命中。事件包含规则、次数、已有的客户端 IP 以及已配置的路由名称。未映射的 URI 路径展示哈希。

| 字段映射 | 巡检内容 |
| --- | --- |
| `ip`、`uri`、`host`、`ua` | 高频访问、异常 Host 或 User-Agent、敏感文件和路径、SQL 注入、XSS、路径遍历特征 |
| `status` 与 `ip` 或 `uri` | 单 IP 的 401、403、404、429 激增，接口级的 429、502、503、504 激增 |
| `method` | 与 `sources[].allowed_methods` 不符的请求方法 |
| `waf`、`wafrule` | WAF 拦截动作与可信 WAF 日志提供的归一化安全分类 |
| `tls`、`upstream`、`latency` | 旧版 TLS、上游错误激增和 P95 延迟异常 |

`rules.nginx_burst` 默认每窗口 20 条，同时应用历史基线倍率。WAF 分类字段应来自已有可信规则，使用相应归一化标识，例如 `ssrf`、`command_injection`、`request_smuggling`、`deserialization` 或 `upload_abuse`。巡检结果表示日志中的信号，需结合 WAF 记录、认证审计或漏洞扫描结果复核。

## VictoriaMetrics

使用已有 VictoriaMetrics 时，注入 `VICTORIAMETRICS_WRITE_URL`。看板和通知需要查询链接时，配置浏览器可访问的 `VICTORIAMETRICS_READ_URL`。两项都填写服务或网关基础地址，写入和查询的 API 路径由应用添加。单节点通常使用相同的服务基础地址，集群版的写入和查询基础路径分别可为 `/insert/0/prometheus` 和 `/select/0/prometheus`。写入网关需要 Bearer Token 时，通过环境变量或 Secret 注入 `VICTORIAMETRICS_WRITE_TOKEN`。查询侧由现有网关负责认证。

本地 Compose 可一起启动 VictoriaMetrics：

```sh
docker compose -f compose.yaml -f compose.victoriametrics.yaml up -d --build
```

默认使用持久卷和 30 天保留期，宿主机回环端口为 8428。远程访问时将 `VICTORIAMETRICS_READ_URL` 改为使用者能访问的内网 HTTPS 地址。应用通过 Prometheus import 接口写入，按 `source`、`kind` 标记窗口样本，并使用窗口结束时间作为时间戳。

- `sre_aiobs_window_logs` 保存窗口内日志条数。
- `sre_aiobs_window_risk_events` 和 `sre_aiobs_window_high_risk_events` 保存事件及高风险事件条数。
- `sre_aiobs_window_max_risk_score` 和 `sre_aiobs_window_evaluated` 保存最高风险分与探测完成标记。

异常链接按对应任务窗口查询时间序列，打开后显示 VictoriaMetrics 的范围查询结果。启用前已完成的任务历史保留在 PostgreSQL，VictoriaMetrics 从启用后完成的窗口开始写入。原始日志与事件明细仍分别由日志源和 PostgreSQL 提供。发送失败会通过现有任务机制重试，同一任务的已成功告警沿用投递去重。样本标签不包含 IP、URI、凭据或 Run ID。

## Grafana 延迟与异常图

给目标日志源映射单位为秒的 `fields.latency`，例如 Elasticsearch 的 `nginx.request_time` 或 Loki JSON 的 `request_time`。应用按原始延迟样本计算每个探测窗口的 P50、P95、P99。每 URI 的分位数继续用于接口分析；总窗口图采用源端整体聚合。缺少采样的窗口留空。

已有 Grafana 时，添加 VictoriaMetrics 为 Prometheus 类型数据源，然后导入 [dashboard.json](../deploy/grafana/dashboard.json)。在仪表盘顶部选择数据源、日志源和窗口粒度。仪表盘提供三张独立分位数图、对比图、P95 与阈值图、P95 越限窗口、风险事件及最高风险评分图。底部的任务和 AI 图表需用 Prometheus 兼容采集器抓取 `/metrics`。

本地可让 Compose 自动配置 VictoriaMetrics 数据源和仪表盘。在 `.env` 中设置强随机的 `GRAFANA_ADMIN_PASSWORD`，然后运行：

```sh
docker compose -f compose.yaml -f compose.victoriametrics.yaml -f compose.grafana.yaml up -d --build
```

Grafana 默认绑定宿主机 `127.0.0.1:3001`。使用 `admin` 和设置的密码登录。通过 `GRAFANA_URL` 指定浏览器能访问的 Grafana 基础地址后，异常链接优先跳转至仪表盘 `sre-aiobs`，并带入任务的日志源、窗口粒度和时间范围。没有配置该地址时，链接沿用 VictoriaMetrics 查询。

## Elasticsearch 与 Nginx 批量采集

新服务器有结构化 Nginx 日志时，在部署包目录启动整套服务及同机 Filebeat：

```sh
NGINX_LOG_DIR=/var/log/nginx NGINX_LOG_GLOB=sre-aiobs.jsonl ./deploy/single-node/start.sh --with-nginx-collector
```

生产部署请在 `.env` 中持久化 `NGINX_LOG_DIR` 和 `NGINX_LOG_GLOB`。默认读取 `/var/log/nginx/access.log`，目录以内只读取匹配规则的 JSON 行。如果 Nginx 已有兼容的 JSON 日志，直接指定文件。旧式 combined 文本日志需继续走已有采集链路，或经审批添加 [JSON 日志格式](../deploy/collectors/nginx-log-format.conf) 并设置单独的 access log。先执行 `nginx -t` 再按现有变更流程重新加载。对已有 `config.yaml`，可把 [预设数据源](../deploy/single-node/config.elasticsearch.yaml) 合并到原配置。

索引和映射随集群初始化：

| 索引模式 | 数据入口 | 探测与查询 |
| --- | --- | --- |
| `nginx-access-*` | 本机或批量 Filebeat，按日志 UTC 日期路由 | Nginx 风险接口、状态码、时延、请求量、客户端 IP 和 GeoIP |
| `app-logs-*` | 现有应用日志采集链路，使用独立写入凭据 | 按日志级别、异常类别和下游故障巡检 |
| `vulnerability-findings-*` | 已有扫描结果发布流程，使用独立写入凭据 | CVE 聚合、小时报告与日报 |

`--with-elasticsearch` 仅启动服务栈与索引初始化。应用和 Grafana 使用索引范围内的只读凭据。Filebeat 写入凭据只覆盖 Nginx 索引。Elasticsearch 数据目录持久化，索引清理和快照请按业务数据保留策略配置。

多台 Nginx 使用以下流程：

1. 在中心服务器的 `.env` 设置 `ES_INGEST_BIND` 为内网地址。将 `ES_INGEST_CERT` 和 `ES_INGEST_KEY` 指向中心机的证书链和私钥文件，默认为 9443 端口。用 `./deploy/single-node/start.sh --with-ingest-gateway` 启动。只允许采集主机通过内网访问该端口。
2. 复制 [示例清单](../deploy/collectors/inventory.example.ini) 为 `deploy/collectors/inventory.ini`，填写真实主机、HTTPS 网关域名以及日志路径。所有采集机需能 SSH 登录，且具有 sudo 和 Docker Compose v2。使用私有 CA 时在清单中填写 `collector_ca_file`，路径指向运行 Ansible 的控制机上的 PEM 文件。
3. 在控制机用 `ansible-vault create deploy/collectors/vault.yml` 创建加密变量文件，填入 `collector_elasticsearch_password`，值取中心服务器 `.env` 的 `ELASTICSEARCH_INGEST_PASSWORD`。运行下列命令，默认每批最多五台。重复执行保留采集位置并应用配置更新。

```sh
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml \
  --ask-vault-pass -e @deploy/collectors/vault.yml
```

每台采集机默认用 inventory 左侧的主机名作为 Filebeat 采集名。要自定义，可为该主机设置 `collector_hostname`，例如 `nginx02 ansible_host=192.0.2.11 collector_hostname=nginx-edge-02`。采集名写入 `agent.name`，并由 Elasticsearch 管道复制至 `collector_hostname`。在 Kibana Discover 的 Nginx 数据视图可按 `collector_hostname : "nginx-edge-02"` 筛选。既有 Nginx 日志主机名仍保留在 `log_hostname`，缺少时才使用采集名回填。更新中心机管道并重新执行 Ansible 后，新日志会附带采集名。

Grafana 登录后打开 `/d/sre-aiobs-nginx` 的「Nginx Access 日志看板」。面板可看每分钟 QPS、错误率、状态码、域名、服务、主机、接口、User-Agent、流量、客户端 IP、GeoIP，以及 P50、P95、P99。默认按附件口径过滤 `/ws/v1/`、`cb` 服务和 ALB 健康检查，可在「流量过滤」修改 Lucene 表达式。巡检异常在 `/d/sre-aiobs/sre-aiobs` 查看。访问 Grafana 的人员可查看日志数据和客户端 IP，请用现有内网访问控制限制账号。

日志需提供真实来源 IP。Nginx 位于代理之后时，应沿用已有的可信代理与 real IP 配置。地区面板需要 Elasticsearch 内置 GeoIP 数据库完成下载并解析公网 IP。离线环境需由管理员提供 GeoIP 数据。服务级和响应体流量图分别依赖 `service` 与 `body_bytes_sent`。新增模板和管道应用于新建索引，既有数据要先核对字段和模板兼容性。应用通知中为单个事件添加地理信息时按本手册前文配置 `geo_db`。

独立的 [Nginx 地理分布看板](../deploy/grafana/nginx-geo-dashboard.json) 由 Compose 自动加载，Grafana 路径为 `/d/sre-aiobs-nginx-geo`。可以查看来源国家数、国家和城市 Top 20、各国流量与 4xx／5xx 趋势，以及按国家、地区和高频 IP 汇总的错误率。该看板包含完整匹配流量。只在具备所需 GeoIP 字段的记录中计算地理排行，来源国家数使用 Elasticsearch 的近似去重。私网、保留地址以及无法定位的公网地址不会产生地理标签。已有索引在切换管道版本后，旧记录的城市和状态分类字段需要按既有回填流程补齐。

## 完整 ELK 单机部署

在 Elasticsearch、PostgreSQL、Redis、VictoriaMetrics、Grafana 和 Go 服务的基础上，`--with-elk` 增加同版本的 Kibana 和 Logstash。要一起启动本机 Nginx 采集，解压后运行：

```sh
NGINX_LOG_DIR=/var/log/nginx NGINX_LOG_GLOB=sre-aiobs.jsonl \
  ./deploy/single-node/start.sh --with-elk --with-nginx-collector
```

本机采集链路为 **Nginx JSON 日志 → Filebeat → Logstash → Elasticsearch**。Logstash 使用持久化队列，默认容量 512 MB，且只拥有 Nginx 索引写入权限。Beats 端口 5044 仅在 Compose 网络内可达。已有的 Ansible 批量采集仍可通过 `--with-ingest-gateway` 启用的内网 HTTPS 网关写入 Elasticsearch。

Kibana 默认绑定 `127.0.0.1:5601`。初次登录使用 `elastic` 和 `.env` 中的 `ELASTIC_PASSWORD`。日常使用请创建受限的个人账号。Kibana 服务内部使用独立的 `kibana_system` 密码，密钥保存在 `.env`，重启时复用。启动后可在 Discover 选择 Nginx、Applications 或 Vulnerabilities 三个预建 Data View。Nginx Data View 沿用两份附件中的 ID。启动时会自动导入 [Nginx Access 日志看板](../deploy/elk/dashboards/sre-aiobs-nginx-access.ndjson) 和 [Nginx 地理分布看板](../deploy/elk/dashboards/sre-aiobs-nginx-geo.ndjson)。登录后在 **Analytics → Dashboard** 打开。再次启动时保留已经安装的同 ID 看板。已有 Kibana 8.17 环境也可在 **Stack Management → Saved Objects → Import** 导入这两个 NDJSON，先确保 Nginx Data View ID 为 `bc589ecf-5117-40ca-b632-cb021652f611` 并指向 `nginx-access-*`。Grafana 看板也会自动加载。

首次启动需拉取 Elastic 官方镜像。可用 `COMPOSE_WAIT_TIMEOUT=480` 延长服务就绪等待。若单机上已经有 `config.yaml`，脚本会保留它，按前文合并本地 Elasticsearch 数据源即可。
