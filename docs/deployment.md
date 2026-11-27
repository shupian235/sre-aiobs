# 配置与部署

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

Compose 注入 `POSTGRES_PASSWORD`、`REDIS_PASSWORD`、`DATABASE_URL` 和 `REDIS_URL`，将 `AIOBS_CONFIG` 指向本地配置后运行 `docker compose up -d --build`。示例配置只展示字段映射，需替换成实际来源。生产迁移建议使用专用账号。运行连接建议启用 PostgreSQL TLS 验证及 Redis TLS。

Kubernetes 推送构建镜像后，替换 `deploy/kubernetes/deployment.yaml` 内的镜像地址。创建 `sre-aiobs-config` ConfigMap，其键为 `config.yaml`，以及运行和迁移用的两个 Secret。挂载 GeoIP 库时更新卷配置。应用清单后检查迁移 Job 和 Deployment 状态。将 `/metrics` 添加到 Prometheus 并导入 Grafana Dashboard。服务内部暴露 `/healthz`、`/readyz`、`/metrics`，保持访问范围在集群监控网络内。

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
