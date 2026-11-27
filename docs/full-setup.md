# AIOps 平台完整搭建流程

本手册从一台 Linux 主机开始，完成 ELK、采集端、AIOps 服务和中间件的部署。已有 Elasticsearch 的环境可以只部署 AIOps 服务与中间件。

## 1. 组件和数据流

| 组件 | 职责 | 默认网络或存储 |
| --- | --- | --- |
| AIOps Go 服务 | 定时巡检、风险规则、Web、历史报告和通知 | Web 为 `127.0.0.1:8080` |
| PostgreSQL | 巡检任务、风险事件、配置、报告和审计 | Compose 内网 5432，数据卷 `pgdata` |
| Redis | 调度锁、去重、登录会话和 AI 配额 | Compose 内网 6379，数据卷 `redisdata` |
| Elasticsearch | 接收和聚合 Nginx、WAF、应用及扫描结果 | Compose 内网 9200，数据卷 `elasticdata` |
| Kibana | 日志查询、QPS、延迟、GeoIP 和 WAF 看板 | `127.0.0.1:5601` |
| Logstash | 接收本机 Filebeat 日志并写入 Elasticsearch | Compose 内网 5044，持久队列 |
| Filebeat | 以只读方式读取 Nginx JSON 日志 | 同机走 Logstash；远程走内网 HTTPS 网关 |
| AWS CloudWatch | 直接通过 AWS SDK 读取云监控告警 | 通过 HTTPS 访问 AWS |
| OpenAI／Claude 和通知渠道 | 可选 AI 分析、Slack／Telegram 告警、SES 报告 | 通过 HTTPS 访问启用的服务 |

## 2. 准备主机和源码

安装 Docker、Docker Compose v2、OpenSSL，确认可访问所需镜像仓库和 Go 模块代理。手工编译需要 Go 1.27.4。完整 ELK 首次启动会下载多个镜像，需要为镜像、数据卷和索引留出磁盘空间。

```bash
docker version
docker compose version
openssl version
git clone https://github.com/shupian235/sre-aiobs.git
cd sre-aiobs
```

使用部署包时，解压并进入其中的 `sre-aiobs` 目录。后续命令均在项目目录执行。

## 3. 生成配置并检查

```bash
./deploy/single-node/start.sh --with-elk --check
```

此命令首次生成权限为 `0600` 的 `.env`、部署用 `config.yaml` 和随机密码，只校验 Compose。升级时复用原文件和项目名。

- `.env` 保存数据库连接、Redis 连接、Elasticsearch 账号凭据与 Web 密码。现有部署保留原密码和连接串。
- `config.yaml` 保存数据源、巡检规则和非敏感选项。内置 Elasticsearch 的地址为 `http://elasticsearch:9200`。接入现有集群时替换地址、索引和只读凭据引用。
- 应用使用只读 Elasticsearch 身份；Filebeat 使用单独的写入身份。已生成的应用账号与 Kibana 系统账号不同。

[四类索引及字段映射](elasticsearch-indexes.md)列出了 Nginx 访问、Nginx 安全、应用日志及 CVE 发现的模板。

## 4. 启动服务和采集

### 单机已有 Nginx JSON 日志

先确认 Nginx 正在写入指定文件。如果目前使用 combined 文本格式，可在测试环境核对 [JSON 日志格式示例](../deploy/collectors/nginx-log-format.conf)，按 Nginx 变更流程添加独立 JSON 日志。把路径替换为实际值，并保存到部署机 `.env`：

```dotenv
NGINX_LOG_DIR=/var/log/nginx
NGINX_LOG_GLOB=sre-aiobs.jsonl
```

```bash
./deploy/single-node/start.sh --with-elk --with-nginx-collector
```

启动顺序为 PostgreSQL／Redis 就绪、执行数据库迁移、初始化 Elasticsearch 索引模板和权限、安装 Kibana 数据视图和看板后启动 AIOps。Filebeat 通过 Logstash 写入访问索引。更换日志文件路径后，用相同选项重新启动。WAF 和漏洞扫描结果分别由自己的采集链路写入对应索引。

### 远程 Nginx 批量采集

中心机在 `.env` 设置 `ES_INGEST_BIND` 为内网地址，并填写 `ES_INGEST_CERT` 和 `ES_INGEST_KEY` 的绝对路径。默认接入端口为 9443，只向采集主机开放。

```bash
./deploy/single-node/start.sh --with-elk --with-ingest-gateway
```

在 Ansible 控制机复制 [清单示例](../deploy/collectors/inventory.example.ini)，填写 SSH 地址、网关 HTTPS 域名和日志路径。将中心机的 `ELASTICSEARCH_INGEST_PASSWORD` 存为 Ansible Vault 的 `collector_elasticsearch_password`。

```bash
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml \
  --ask-vault-pass -e @deploy/collectors/vault.yml
```

采集名默认使用清单主机名，单台可设置 `collector_hostname`。同一中心机也可以同时开启 `--with-nginx-collector`，采集本机日志。[批量采集详情](deployment.md#elasticsearch-与-nginx-批量采集)

### 已有 Elasticsearch 或外置 PostgreSQL／Redis

已有 Elasticsearch 时，使用 `./deploy/single-node/start.sh --check` 生成 AIOps 基础配置。为已有集群配置只读账号，并将来源加入 `config.yaml`（仅当允许无源启动时才保留 `allow_empty_sources: true`）。接入完成后执行 `./deploy/single-node/start.sh`，启动应用和本机 PostgreSQL／Redis。

若 PostgreSQL 与 Redis 也由现有环境提供，使用单独的 [Kubernetes 清单](../deploy/kubernetes/) 或独立 Go 进程，通过 Secret 注入 `DATABASE_URL` 与 `REDIS_URL`。先构建并迁移，再启动应用：

```bash
go build -o sre-aiobs ./cmd/sre-aiobs
./sre-aiobs -migrate ./migrations
./sre-aiobs -config ./config.yaml
```

升级新增索引时，数据库迁移会在创建索引期间锁定相关表写入。已有大表应安排维护窗口。[外部数据库和 Kubernetes 部署](deployment.md#kubernetes)

## 5. 登录和配置业务能力

| 配置项 | 在哪里设置 |
| --- | --- |
| AIOps Web 登录 | 本机 `http://127.0.0.1:8080`，账号 `operator`，密码为 `.env` 的 `DASHBOARD_PASSWORD` |
| Kibana 登录 | 本机 `http://127.0.0.1:5601`，初始账号 `elastic`，密码为 `.env` 的 `ELASTIC_PASSWORD` |
| Google Authenticator | 部署 Secret 注入 `DASHBOARD_TOTP_SECRET`，重启后通过 HTTPS 从 `/login` 登录 |
| Nginx / WAF 自动巡检 | `config.yaml` 的 `sources` 与 `rules`，结果在 Web「Nginx 安全巡检」 |
| AWS 云端巡检 | 通过 IAM Role 或 `.env` 的 AWS 凭据链接入，在 `sources` 配置 `kind: aws` 和区域 |
| AI 分析 | `config.yaml` 选 `ai.provider` 为 `openai` 或 `claude` 并填写模型；密钥分别注入 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY` |
| Slack／Telegram | 部署环境注入 `SLACK_WEBHOOK_URL` 或 `TELEGRAM_BOT_TOKEN`；Telegram 另填 `telegram_chat`。在 Web 启用渠道 |
| SES 邮件 | Web「告警设置」填发件地址、收件地址和可选区域，勾选小时报告与日报。AWS IAM 提供发送权限 |
| 告警阈值和冷却 | Web「告警设置」修改，保存时用 `ALERT_SETTINGS_PASSWORD` |

AWS 巡检目前读取 CloudWatch 已配置的指标告警。使用临时 Access Key 时，同时注入 `AWS_SESSION_TOKEN`；邮箱地址在 Web 保存。[AWS 配置与权限](aws-inspection.md) · [Web 登录和告警配置](web-operations.md)

远程查看可建立 SSH 隧道：

```bash
ssh -N -L 8080:127.0.0.1:8080 -L 5601:127.0.0.1:5601 user@server
```

## 6. 验收

```bash
curl -fsS http://127.0.0.1:8080/readyz
docker compose --env-file .env -f compose.yaml ps
docker compose --env-file .env -f compose.yaml logs --tail=100 app migrate
```

1. Kibana **Discover** 看 Nginx 是否有新日志，按实际时间范围筛选。多机采集可用 `collector_hostname` 区分。
2. AIOps **日志数据源 → 检查** 确认 Elasticsearch 查询和 AWS 只读连接。最近一分钟零条表示该时间段没有匹配日志。
3. 从 **任务记录** 点开 Run ID，检查事件、高风险数量、延迟或 CloudWatch 状态汇总。按需下载 HTML、JSON、Markdown。
4. 若启用 SES，在小时或日任务完成后检查报告的「投递状态」。Kibana 安全看板从 `nginx-security-*` 读取 WAF 日志，需单独验证该来源已写入数据。

## 7. 备份与升级

升级前备份 `.env` 与 `config.yaml`，备份 PostgreSQL 和 Elasticsearch 数据。使用快照保存 Elasticsearch 索引。对内置 PostgreSQL，可执行：

```bash
mkdir -p backups
docker compose --env-file .env -f compose.yaml exec -T postgres \
  pg_dump -U aiobs -d aiobs -Fc > "backups/aiobs-$(date +%F-%H%M).dump"
```

更新版本后采用原有部署参数启动，迁移容器会按顺序应用 SQL。升级后重复验收。停止或排障时保留数据卷；清理前先核查备份和恢复流程。
