# SRE 巡检整合交付记录

## 现有实现与本阶段改动

| 模块 | 已有能力 | 本阶段新增 |
| --- | --- | --- |
| 日志与数据源 | Elasticsearch Composite 分页与只读连接检查 | 支持按环境和索引配置 |
| Nginx 巡检 | IP、URI、Host、UA、状态码、敏感路径、动态基线、事件关联与地区 | 按来源自动应用已保存的路由规则或 YAML 默认规则 |
| 后端业务 | 严重错误、异常类型、依赖故障，按日志来源组织 | 可选 WARN 激增和业务错误码激增，业务标识对外使用哈希 |
| 性能 | 聚合请求量、QPS 和分位数 | Kibana 与巡检报告展示 P50、P90、P95、P99 |
| 执行与报告 | Go 定时任务、Run ID、分布式锁、任务重试、审计、OpenAI／Claude、Telegram／Slack／SES、HTML 邮件 | 任务记录可直接预览与下载历史报告，显示独立的通知投递状态 |

## 文件和迁移

- `migrations/003_source_routes.sql` 创建 `source_route_settings`，每个数据源保存一个有序 JSONB 数组。无记录时继承 YAML；空数组显式禁用映射。
- `internal/store/routes.go` 在同一事务中保存或清除覆盖项并写入 `audit_logs`。
- `internal/dashboard` 提供兼容已有调用的路由设置 API 和统一数据源检查；`internal/worker` 在解析新风险事件前读取对应来源的覆盖项。
- `internal/store/report.go` 和 `internal/dashboard/reports.go` 从现有数据表只读生成历史报告，无需新建报告表。
- `internal/source`、`internal/detect`与 `deploy/elk/dashboards` 提供只读日志查询、检测规则及分位数。
- 完整 YAML 从 [config.example.yaml](../config.example.yaml) 开始调整。ELK 部署见 [快速部署](quickstart.md)。

## 配置及 IAM

连接检查可选填 `check_timeout`（默认 3 秒，最大 4 秒）及 `check_retries`（默认 0，最多 2 次），重试包含在同一个超时窗口内。同一实例最多并发四个手动检查。

业务来源以 `name` 区分，例如 AAPI、UAPI、MAPI、CBGame、KeyHub。按实际日志映射 `fields.level`、`fields.exception`、`fields.dependency` 和 `fields.businesscode`。其中 `businesscode` 仅映射在业务请求失败时出现的分类字段。配置 `rules.backend_warnings` 或 `rules.business_errors` 为正整数后启用各自规则，比较窗口计数与历史基线。

## API

| 方法与路径 | 用途 |
| --- | --- |
| `GET /api/sources` | 返回已配置来源以及是否支持路由编辑 |
| `POST /api/sources/{name}/check` | 执行只读检查，需带 `X-SRE-AIObs-Check: 1`；仅日志来源返回最近一分钟计数 |
| `GET /api/sources/{name}/routes` | 获取生效规则、覆盖状态和可编辑状态 |
| `PUT /api/sources/{name}/routes` | 保存有序数组，例如 `[{"label":"/api/orders/:id","pattern":"^/api/orders/[0-9]+$"}]` |
| `DELETE /api/sources/{name}/routes` | 清除已保存的覆盖，恢复 YAML 规则 |
| `GET /api/reports/{run}` | 预览历史报告，支持 `?format=html`、`json`、`markdown` 下载 |

API 沿用看板认证。路由写接口还需 `X-Alert-Settings-Password`。保存与重置成功返回 `204`，无 URI 映射的数据源返回 `404`，规则不合法返回 `400`。更新只影响后续分析，已入库事件保留原路由标识。在「任务记录」点击 Run ID 可查看报告，报告展示窗口、尝试次数、日志量、延迟、风险类别、AI 分析和投递状态。没有采集数据时，JSON 的 `log_count` 为 `null`，预览明确标记数据缺失。

## 升级

先在测试环境备份数据库，使用 Compose 部署时以原选项运行启动脚本，迁移容器应用新增 SQL。Kubernetes 或独立进程先运行新版本的 `sre-aiobs -migrate /migrations`，再更新服务。`/readyz` 会检查新增表。加载状态正常后，在看板核对数据源，并观察后续自动巡检记录。已经保存的覆盖项在应用降版后仍留在数据库。

## 尚未完成

- 手动与自定义时间窗口的集中任务 API、服务管理、持久排队、取消和部分成功状态，以及按 Asia/Singapore 的可配置日／周报调度。
- XFF 可信代理链校验、ASN、完整跨源关联、Trace ID 上下文、发布前后比较、按服务域名接口的完整性能趋势和全部 Top 20。
- AWS 资源健康巡检、Kubernetes 资源巡检及周期性监控状态持久化。
- 统一文本与嵌套数据脱敏、扩展 AI 根因与历史相关性、跨源综合报告和报告重发、Telegram 恢复与周报、独立通知服务。
- 多用户 RBAC、完整新增菜单、systemd／Helm 发布及完整生产规模集成测试。

## 验证

本地验证包含 `go test ./...`、`go vet ./...`、Go 服务编译与 JavaScript 语法检查。测试覆盖路由权限、无效表达式、空覆盖与回退、Elasticsearch 只读聚合以及报告数据缺失与 HTML 转义。启动临时 PostgreSQL 测试容器时，云端 Docker 存储空间不足。真实 PostgreSQL 迁移与完整 Compose 集成测试需要在有可用空间的测试环境运行。
