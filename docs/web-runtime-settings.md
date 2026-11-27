# AIOps 界面配置与加密

| 配置 | 界面位置 | 保存方式 |
| --- | --- | --- |
| AWS CloudWatch 来源、区域、账号、告警前缀、Access Key／Secret Key／STS Token | 运行配置 | PostgreSQL AES-256-GCM |
| IP／URI／状态码阈值、P95、历史基线、后端业务和依赖错误、Nginx 突发异常阈值 | 运行配置 | PostgreSQL AES-256-GCM |
| OpenAI／Claude、模型、每日请求上限 | 运行配置 | PostgreSQL AES-256-GCM |
| 采集主机名、SSH 地址／端口／用户、网关、日志位置及采集写入密码 | 采集部署 | PostgreSQL AES-256-GCM |
| SES 发件人、收件人和区域，覆盖安全巡检 HTML 报告目的地 | 告警设置 | PostgreSQL AES-256-GCM |
| 告警评分、冷却、Slack／Telegram／邮件开关 | 告警设置 | 兼容原 PostgreSQL JSON 配置 |
| 部署任务编号、时间、配置版本、主机数、结果 | 采集部署 | PostgreSQL 任务记录，不含凭据或命令输出 |

平台监听地址、Elasticsearch 只读源及索引映射、GeoIP 数据库、PostgreSQL／Redis 连接、AI API Key、Telegram／Slack 凭据、SES IAM、SSH 私钥及主机指纹、网关 TLS 和配置加密主密钥由部署端管理。限制 AIOps 控制机的网络出口，仅向已批准的采集 SSH 地址开放。采集机器需要事先准备 SSH、Python 3、sudo、Docker Compose v2 和 Nginx JSON 日志。

## 首次启用与升级

单机启动脚本会在 `.env` 生成并保留 `AIOBS_SETTINGS_KEY`。其他部署方式用 `openssl rand -base64 32` 生成一次，将值交给 Secret 管理系统。所有实例使用同一个密钥。设置 `DASHBOARD_PASSWORD` 与 `ALERT_SETTINGS_PASSWORD`，设置写入和运行采集任务均需要管理密码。

Compose 启动会运行数据库迁移。手动部署应先对现有数据库执行 `sre-aiobs -migrate ./migrations`。迁移 005 添加密文存储，应用启动时把旧邮件目的地迁至密文列。迁移 006 添加 Ansible 任务历史和并发约束。密钥和数据库备份分开保管。更换或遗失旧密钥会使已保存的密文无法读取。旧数据库备份和 WAL 可能保留升级前的邮件地址。

## 使用界面

1. 登录，打开「运行配置」。AWS 来源字段填写数组，例如 `[{"name":"cloud-prod","environment":"production","region":"ap-southeast-1","account_id":"","alarm_prefix":""}]`。Web 新增的 AWS 来源可共用一套只读 AWS 凭据。留空使用部署身份，配置 YAML 内的 AWS 来源和 SES 继续使用原部署身份。保存后巡检在后续周期加载配置。
2. 在「运行配置」启用需要覆盖的巡检阈值和 AI 设置。未覆盖的规则继续使用部署配置。
3. 在「告警设置」选择渠道，填写并保存 SES 发件和收件地址。发送使用已授权的 SES 部署身份。
4. 通过 [先启动 AIOps、再批量采集](full-setup.md#远程-nginx-批量采集) 配置 SSH 控制端。打开「采集部署」，填写写入网关、日志目录、写入密码和 SSH 用户。逐行录入主机名、地址和端口，最多 50 台，一次保存。采集主机名会写入 Filebeat 配置。
5. 点击「检查连接」验证 SSH 指纹、sudo、Docker Compose 和日志目录。检查成功后点击「批量部署 Filebeat」并确认。任务使用上一次保存的配置版本，按每批 5 台执行。最近任务显示 Run ID 和结果。部署成功表示 Playbook 执行成功，数据到达情况通过 Elasticsearch 索引或 Kibana 确认。失败可能已经更新部分主机，修复环境后可按相同清单重新部署。

## 接口

所有请求需要看板登录。写请求发送 `Content-Type: application/json` 及 `X-Alert-Settings-Password`。界面凭据通过 HTTPS 或 SSH 隧道提交。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/runtime-settings` | 读取配置版本和 AWS 凭据设置状态，响应排除凭据 |
| PUT | `/api/runtime-settings` | 保存 AWS、阈值、AI 设置，提交当前 `version` |
| GET | `/api/collectors` | 读取主机清单、配置版本、写入密码状态和最近部署记录 |
| PUT | `/api/collectors` | 保存采集配置，提交 `version`、`hosts`、`user`、`gateway`、`log_dir`、`log_glob` 和首次写入的 `password` |
| POST | `/api/collectors/runs` | 提交 `{"action":"check","version":1}` 或 `{"action":"deploy","version":1}`，返回 202 和任务 ID |
| GET／PUT | `/api/alert-settings` | 查看及保存告警渠道开关、阈值和 SES 目的地 |

配置版本冲突和重复提交运行任务返回 HTTP 409。同一数据库的实例最多执行一个采集任务。Ansible 命令限时 6 分钟，异常退出造成的无主记录在 8 分钟后视为失败。界面部署调用仓库内置 playbook，采集写入密码留空保留。Ansible 临时清单和附加变量使用私有目录，Compose 控制端将该目录放在内存文件系统。
