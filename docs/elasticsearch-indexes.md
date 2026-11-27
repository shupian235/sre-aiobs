# Elasticsearch 索引与安全巡检

完整 ELK 启动时会安装下列四类索引模板、只读角色与 Kibana Data View。新索引第一次收到数据时使用对应模板。单机模板使用一个主分片、零副本；生产集群应按容量和可用性调整模板。现有索引的类型映射和历史数据需要按原环境的迁移流程处理。

| 索引模式 | 索引名示例 | 写入方 | Kibana Data View | 巡检用途 |
| --- | --- | --- | --- | --- |
| `nginx-access-*` | `nginx-access-2026.11.27` | 本机 Filebeat → Logstash，或批量 Filebeat 经 HTTPS 网关 | Nginx | 请求量、QPS、状态码、接口延迟、GeoIP、IP 和 URI 风险检测 |
| `nginx-security-*` | `nginx-security-2026.11.27` | 已有 WAF 或安全日志采集器 | Nginx Security | WAF 拦截、安全分类和客户端来源；运维看板汇总规则命中 |
| `app-logs-*` | `app-logs-orders-2026.11.27` | 应用日志采集链路 | Applications | 严重错误、WARN 激增、异常类型、依赖故障、业务错误码 |
| `vulnerability-findings-*` | `vulnerability-findings-2026.11.27` | 可信扫描器或 CI 发布任务 | Vulnerabilities | 聚合 CVE，生成小时报告和日报 |

日期后缀是示例，匹配同一个索引模式即可。所有来源使用 `@timestamp`（类型 `date`）；写入时提供带时区的事件时间。完整映射见 [Nginx 访问模板](../deploy/elasticsearch/nginx-index-template.json)、[安全日志模板](../deploy/elasticsearch/security-index-template.json)、[应用模板](../deploy/elasticsearch/application-index-template.json)、[漏洞模板](../deploy/elasticsearch/vulnerability-index-template.json)。

## Nginx 访问和安全字段

| 字段 | 类型 | 用途 |
| --- | --- | --- |
| `client_ip` | `ip` | 客户端 IP 聚合。经过代理时，应由可信链路生成真实客户端地址 |
| `request_uri`、`request_path` | `keyword` | 源端巡检使用 URI，Kibana 按不含查询串的路径展示接口 |
| `nginx_host`、`user_agent`、`method` | `keyword` | Host、User-Agent、HTTP 方法分析 |
| `status`、`upstream_status` | `integer`、`keyword` | HTTP 状态与上游故障 |
| `request_time`、`body_bytes_sent` | `double`、`long` | 延迟以秒为单位，响应体大小以字节为单位 |
| `tls_protocol` | `keyword` | TLS 版本巡检 |
| `client_geo.country_iso_code`、`client_geo.region_iso_code`、`client_geo.country_name`、`client_geo.city_name` | `keyword` | 国家、地区和城市，来自 GeoIP 管道或可信来源 |
| `log_hostname`、`collector_hostname`、`service` | `keyword` | 日志主机、采集机、业务服务 |
| `error_rate_sample`、`status_4xx_sample`、`status_5xx_sample` | `short` | 由管道生成的错误统计辅助字段 |
| `waf_action`、`waf_category`、`waf_rule_id` | `keyword` | 安全动作、归一化分类、原 WAF 规则标识。前两项参与规则巡检 |

安全日志可以只含时间、真实客户端 IP、动作和分类。下面是字段映射样例，供采集器对齐格式：

```json
{"@timestamp":"2026-11-27T03:00:00Z","client_ip":"192.0.2.24","service":"orders","waf_action":"blocked","waf_category":"upload_abuse","waf_rule_id":"example-rule"}
```

在已有部署的 `config.yaml` 加入 [安全来源示例](../config.example.yaml)。内置 ELK 首次生成的配置已包含该来源。WAF 采集器应使用单独凭据，由管理员分配 `sre_aiobs_security_ingest` 角色或等价的受限 API Key，以 `create` 方式写入。部署脚本不会给默认 Nginx Filebeat 账号增加安全索引写权限。若动作和分类已写在访问日志里，也可以在访问源映射 `waf: waf_action` 和 `wafrule: waf_category`。同一记录只选择一条巡检来源，避免重复计数。

在 Kibana **Analytics → Dashboard** 打开「Nginx 安全巡检日志看板」，查看拦截数、分类、IP 和地区；在运维看板 **Nginx 安全巡检** 按来源筛选告警。Elasticsearch 保存输入日志；应用生成的风险事件、任务和巡检报告存放在 PostgreSQL，可通过看板按 Run ID 查看。

## 应用日志和漏洞字段

- **应用日志**使用 `service`、`level`、`exception_type`、`dependency_failure`、`business_error_code`（均为 `keyword`）。只在发生失败时上报依赖和业务错误分类；避免把自由文本、凭据或请求正文当作分类值。
- **漏洞发现**使用 `vulnerability_id`、`severity`、`service`（均为 `keyword`）。每条记录包含一个 CVE，并保留实际扫描时间。

应用读取账号的权限范围覆盖上述四类索引，使用 `read`、`view_index_metadata`。各写入方按自身的目标索引单独授权。Kibana 用户权限也应只覆盖其需要查看的视图和索引。
