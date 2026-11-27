# AIOps 平台完整搭建流程

本手册从一台 Linux 主机开始，先启动 AIOps 与中间件，再通过界面部署 ELK 采集端。已有 Elasticsearch 的环境可以只部署 AIOps 服务与中间件。

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

**1. 先启动 AIOps 和 ELK**

```bash
./deploy/single-node/start.sh --with-elk
```

打开 `http://127.0.0.1:8080/`，使用 `operator` 和 `.env` 中的登录密码。若从其他电脑访问，通过 SSH 隧道转发中心机的 8080，或通过内网 HTTPS 反向代理访问。

**2. 启用 HTTPS 写入网关和 Ansible 控制端**

在中心机准备专用 SSH 私钥和已经通过带外方式核验的 `known_hosts`。按实际主机地址填写指纹，非 22 端口使用 `[host]:port` 形式。以下路径是示例。控制端容器以 UID/GID 65532 运行，需要读权限；私钥仅对该用户可读。采集主机的部署账号需允许使用专用密钥登录并执行当前 playbook 所需的非交互 sudo。

```text
/etc/sre-aiobs/collector-ssh/
  id
  known_hosts
  ca.crt                 # 使用内网 CA 时
```

在中心机 `.env` 配置网关、TLS 和只读挂载目录。证书覆盖网关域名，防火墙仅向授权采集机开放 9443。

```dotenv
ES_INGEST_BIND=10.0.0.10
ES_INGEST_PORT=9443
ES_INGEST_CERT=/etc/sre-aiobs/tls/fullchain.pem
ES_INGEST_KEY=/etc/sre-aiobs/tls/privkey.pem
AIOBS_COLLECTOR_SSH_DIR=/etc/sre-aiobs/collector-ssh
# 网关使用公共 CA 时省略下一行。
AIOBS_COLLECTOR_CA_FILE=/run/aiobs-ssh/ca.crt
```

```bash
./deploy/single-node/start.sh --with-elk --with-ingest-gateway --with-web-collectors
```

**3. 从 AIOps 批量部署**

打开「采集部署」，填写采集机可访问的 HTTPS 网关地址、Nginx 日志目录、日志文件名、SSH 用户和 `.env` 中的 Elasticsearch 采集写入密码。把主机名、IP 或 DNS 地址和 SSH 端口添加到清单。批量保存后执行「检查连接」，确认通过后点击「批量部署 Filebeat」。在最近部署任务查询结果，随后到 Kibana 查看新进日志和采集主机名。其余界面配置见 [AIOps 界面配置与加密](web-runtime-settings.md)。

#### 命令行部署方式

命令行方式可用于未启用 Web 控制端的环境。

**2. 准备 Ansible 控制机和采集机**

控制机安装 Ansible，确认 `ansible-playbook --version` 和 `ansible-vault --version` 可用。采集机需提供 SSH、Python 3、sudo、Docker Compose v2，以及实际可读取的 Nginx JSON 日志。确认采集机可访问中心机网关和 Filebeat 镜像仓库。

在控制机进入项目目录，首次创建私有清单：

```bash
cp -n deploy/collectors/inventory.example.ini deploy/collectors/inventory.ini
```

按实际环境编辑 `deploy/collectors/inventory.ini`：

```ini
[nginx_collectors]
nginx01 ansible_host=192.0.2.10
nginx02 ansible_host=192.0.2.11 collector_hostname=nginx-edge-02

[nginx_collectors:vars]
ansible_user=deployer
collector_elasticsearch_url=https://logs.internal.example:9443
collector_log_dir=/var/log/nginx
collector_log_glob=sre-aiobs.jsonl
```

采集名默认取清单左侧的主机名，可为单台设置 `collector_hostname`。日志路径或登录用户不同，也可在对应主机行覆盖。网关使用私有 CA 时，添加 `collector_ca_file=/绝对路径/ca.pem`，该路径位于 **Ansible 控制机**。登录口令、私钥和 Elasticsearch 密钥不写进 inventory。

**3. 创建 Vault 并检查连接**

```bash
ansible-vault create deploy/collectors/vault.yml
```

在打开的编辑器写入以下变量，将占位符替换为中心机 `.env` 的 `ELASTICSEARCH_INGEST_PASSWORD`。保存后 Vault 会加密文件。已有 Vault 时用 `ansible-vault edit` 更新。

```yaml
collector_elasticsearch_password: "替换为中心机生成的写入密码"
```

```bash
ansible -i deploy/collectors/inventory.ini nginx_collectors -m ping
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml --list-hosts
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml --syntax-check
```

**4. 先试一台，再批量部署**

```bash
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml \
  --ask-vault-pass -e @deploy/collectors/vault.yml --limit nginx01

ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml \
  --ask-vault-pass -e @deploy/collectors/vault.yml
```

若 sudo 要密码，加 `--ask-become-pass`。Playbook 每批最多五台，配置安装在采集机的 `/opt/sre-aiobs-collector`；重复执行会更新配置并复用采集进度。

**5. 验证采集**

登录采集机查看容器状态与日志：

```bash
cd /opt/sre-aiobs-collector
sudo docker compose ps
sudo docker compose logs --tail=80 nginx-collector
```

在 Kibana **Discover → Nginx** 选择覆盖新日志的时间范围，用 `collector_hostname : "nginx-edge-02"` 核对主机来源。没有数据时，先确认 Nginx 正在写入匹配的 JSON 文件，再检查采集机到网关的 HTTPS 连通性、证书和写入凭据。[采集与索引详情](deployment.md#elasticsearch-与-nginx-批量采集)

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
