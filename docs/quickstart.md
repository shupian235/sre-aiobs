# 单机快速部署

如果只是想看界面，先打开 [离线预览](preview/README.md)。以下步骤适合在一台 Linux 服务器启动完整 ELK 和 sre-aiobs。

## 1. 准备环境

安装 Docker、Docker Compose v2 和 OpenSSL，确认服务器能拉取基础镜像及 Go 模块，并给 Elasticsearch 和 Logstash 留出磁盘空间。准备项目部署包或 Git 工作目录。已有仓库时，可在干净的已提交工作目录生成部署包。

```bash
./deploy/single-node/package.sh
```

## 2. 解压并启动

```bash
tar -xzf sre-aiobs-single-node.tar.gz
cd sre-aiobs

# 按实际路径填写。示例读取 /var/log/nginx/sre-aiobs.jsonl
NGINX_LOG_DIR=/var/log/nginx NGINX_LOG_GLOB=sre-aiobs.jsonl \
  ./deploy/single-node/start.sh --with-elk --with-nginx-collector
```

采集器读取每行一条记录的 Nginx JSON 日志。使用普通 combined 文本日志的服务器，可沿用已有采集链路，或按变更流程添加 [Nginx JSON 日志格式](../deploy/collectors/nginx-log-format.conf)。建议把实际 `NGINX_LOG_DIR` 和 `NGINX_LOG_GLOB` 保存在部署机的 `.env` 中，便于下次启动复用。

| 场景 | 启动选项 |
| --- | --- |
| 单机 ELK 与本机 Filebeat | `--with-elk --with-nginx-collector` |
| 先启动 ELK，稍后接入采集 | `--with-elk` |
| ELK 中心接入多台远程 Filebeat | `--with-elk --with-ingest-gateway` |
| 已有 Elasticsearch 或 Loki | 运行 `./deploy/single-node/start.sh`，按 [数据源说明](deployment.md#日志源) 配置服务 |

脚本首次生成 `.env` 和 `config.yaml`，后续复用文件与数据卷。已有配置的服务器要把 [内置 Elasticsearch 源](../deploy/single-node/config.elasticsearch.yaml) 合并进自己的配置。可在选项末尾加 `--check` 先校验 Compose，该模式会初始化本地配置但不会启动容器。

## 3. 打开界面并确认有数据

以下地址默认绑定部署机本机。初始登录密码从部署机的 `.env` 中查看。

| 界面 | 本机入口 | 初始账号及密码变量 |
| --- | --- | --- |
| 运维看板 | `http://127.0.0.1:8080/` | `operator`，`DASHBOARD_PASSWORD` |
| Grafana | `http://127.0.0.1:3001/` | `admin`，`GRAFANA_ADMIN_PASSWORD` |
| Kibana | `http://127.0.0.1:5601/` | `elastic`，`ELASTIC_PASSWORD` |

在自己电脑查看远程服务器时，先建立 SSH 隧道，然后用相同的本机地址打开网页：

```bash
ssh -N -L 8080:127.0.0.1:8080 -L 3001:127.0.0.1:3001 \
  -L 5601:127.0.0.1:5601 user@server
```

Kibana 启动时会安装两份 Nginx 看板，在 **Analytics → Dashboard** 打开「Nginx Access 日志看板」和「Nginx 地理分布看板」。看板已安装但暂无数据时，先在 Kibana **Discover → Nginx** 选择覆盖日志时间的范围，确认有记录。再到运维看板 **日志数据源 → 检查** 核对最近一分钟计数，在 Grafana 的 **Nginx Access 日志看板**看 QPS 与 P50／P95／P99，在 **Nginx 地理分布看板**看国家、城市和 IP。地区指标需要可用的 GeoIP 数据与正确的客户端 IP。

如果页面打不开，在部署机运行 `curl -fsS http://127.0.0.1:8080/readyz` 并核对启动命令输出的容器状态。如果 Kibana 能打开但没有日志，先核对挂载目录、文件名及 JSON 格式，再检查 Discover 的时间范围。

## 4. 多机采集

1. 在中心机的 `.env` 设置 `ES_INGEST_BIND` 为内网地址，填写 `ES_INGEST_CERT` 和 `ES_INGEST_KEY` 的绝对路径。用 `./deploy/single-node/start.sh --with-elk --with-ingest-gateway` 启动。
2. 在 Ansible 控制机复制 [采集清单示例](../deploy/collectors/inventory.example.ini) 为 `deploy/collectors/inventory.ini`，填写采集机 SSH 地址、中心机 HTTPS 网关和日志路径。用 Ansible Vault 创建 `deploy/collectors/vault.yml`，其中 `collector_elasticsearch_password` 对应中心机 `.env` 的 `ELASTICSEARCH_INGEST_PASSWORD`。
3. 用以下命令部署。采集机需要 SSH、sudo 和 Docker Compose v2。使用私有 CA 时在清单填写 `collector_ca_file`。

```bash
ansible-playbook -i deploy/collectors/inventory.ini deploy/collectors/filebeat.yml \
  --ask-vault-pass -e @deploy/collectors/vault.yml
```

[完整部署手册](deployment.md) · [界面和图表预览](preview/README.md)
