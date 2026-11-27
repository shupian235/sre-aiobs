# 更新与配置说明

## 本次更新

| 模块 | 更新内容 | 配置入口 |
| --- | --- | --- |
| 构建与安全 | Go 升级到 1.27.4，Docker 构建镜像同步。此前更新了 pgx、go-redis、x/text 和 x/sys，并收紧 HTTP 连接限制、日志错误输出和 `.env` 权限。 | [go.mod](../go.mod) · [Dockerfile](../Dockerfile) · [安全审查](security-review.md) |
| 看板登录 | 增加 Google Authenticator 动态码和 Redis 会话，支持退出与防重放。 | `.env` 的 `DASHBOARD_TOTP_SECRET` |
| 数据源 | Elasticsearch、Loki、Prometheus、VictoriaMetrics、CloudWatch、Kubernetes 支持统一只读连接检查。 | `config.yaml` 的 `sources` |
| 巡检与报告 | 新增可选 WARN 和业务错误码激增检测、P90 指标，以及历史报告预览和 HTML、JSON、Markdown 下载。 | `config.yaml` 的 `rules`、Grafana 和看板的「任务记录」 |

## 部署升级

先备份部署目录的 `.env`、`config.yaml` 和 PostgreSQL 数据。升级时使用原来的目录和启动选项，确保继续使用现有配置和数据卷。例如，已使用 ELK 和本机 Nginx 采集的部署可运行：

```bash
git pull --ff-only origin main
./deploy/single-node/start.sh --with-elk --with-nginx-collector
curl -fsS http://127.0.0.1:8080/readyz
```

Compose 会先执行迁移再启动应用。新增的 [003_source_routes.sql](../migrations/003_source_routes.sql) 创建路由覆盖表。独立进程或 Kubernetes 部署应在更新应用前使用新版程序执行 `sre-aiobs -migrate /migrations`。在源码目录本地构建时使用 Go 1.27.4 或更高版本。使用 Docker 构建时，服务器通过构建镜像取得工具链。

## 启用双重验证

1. 在部署机用 `openssl rand 20 | base32` 生成密钥。
2. 在 Google Authenticator 选择「输入设置密钥」，账号可填 `SRE AIObs:operator`，类型选择「基于时间」。将同一密钥写入部署机 `.env` 的 `DASHBOARD_TOTP_SECRET`，或在 Kubernetes 的 `sre-aiobs-env` Secret 注入。
3. 用原选项重新运行部署命令，经 HTTPS 打开运维看板，使用 `operator`、`DASHBOARD_PASSWORD` 和手机上的六位动态码登录。

该变量留空时沿用 Basic Auth。启用后，原先使用 Basic Auth 的看板 API 调用需要改用会话。Kibana 和 Grafana 管理各自的登录配置。[详细启用步骤](quickstart.md#可选启用登录双重验证)

## 配置数据源与巡检

配置日志数据源后，平台会按既有周期自动查询聚合数据并执行巡检。

在 `config.yaml` 的 `sources` 添加需要检查的来源。例如，将下面条目追加到现有列表：

```yaml
- name: prom-prod
  kind: prometheus
  environment: production
  url: https://prometheus.example.net
  check_timeout: 4s
  check_retries: 1
- name: aws-prod
  kind: cloudwatch
  environment: production
  region: ap-southeast-1
```

在 **数据源 → 检查** 验证连接。CloudWatch 通过 AWS SDK 凭据链调用 `cloudwatch:ListMetrics`，生产可用只读 IAM Role。Kubernetes 检查访问 `GET /version`，配置受信 CA 与只读凭据。CloudWatch 和 Kubernetes 当前提供连接检查，资源级巡检尚未实现。各数据源示例见 [配置模板](../config.example.yaml)。

将 `rules.backend_warnings` 或 `rules.business_errors` 设为正整数可启用对应的激增检测。业务错误来源还需配置 `fields.businesscode`，只映射失败时出现的分类字段。P90 依赖日志来源已有的延迟字段映射。在 **任务记录** 点击 Run ID 可打开报告并选择下载格式。[API 与迁移详情](inspection-integration.md)

## 验证范围

Go 1.27.4 下已通过模块校验、单元测试、静态检查和编译，并已核对 Docker 构建镜像标签。动态验证码登录、重放拦截和会话退出已通过 Redis 测试。路由迁移的 PostgreSQL 实机验证和完整 Compose 集成测试仍待在有足够 Docker 存储空间的测试环境执行。

## GeoIP 国家和地区

风险事件和 Nginx 安全巡检列表会显示客户端 IP 的国家与地区。新建的单机 Elasticsearch 配置直接读取采集管道写入的 GeoIP 字段。现有 Elasticsearch 配置可在对应日志源的 `fields` 中追加以下字段，实际索引须有可聚合的 ISO 代码字段：

```yaml
country: client_geo.country_iso_code
region: client_geo.region_iso_code
```

也可通过 `geo_db` 配置可读的 GeoLite2 City 等兼容 MMDB 文件路径，供 Elasticsearch 或 Loki 来源查询。使用容器时，先把文件以只读方式挂载到相同路径。MMDB 查到的国家优先，其余情况读取 Elasticsearch 聚合结果。私网地址或没有地理结果的事件显示「—」。新巡检保存国家与地区，历史事件沿用此前记录的数据。
