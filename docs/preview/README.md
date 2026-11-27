# ELK 与运维看板预览

[离线演示](index.html)可下载后直接在浏览器打开，使用模拟事件。下方图片是功能示意或模拟指标。接入真实数据的步骤见[快速部署](../quickstart.md)。

## ELK 日志链路

![采集、聚合和巡检架构](images/architecture.png)

![单机及批量采集部署](images/deployment.png)

## Kibana 看板

完整 ELK 首次启动后，在 **Analytics → Dashboard** 打开内置的 Nginx Access、地理分布和安全巡检看板。Discover 中可按 `collector_hostname` 筛选远程采集机。

![Kibana 看板和数据视图](images/kibana-dashboards.png)

![Kibana WAF 拦截趋势与异常来源](images/kibana-security.png)

![QPS、状态码和接口流量](images/kibana-access.png)

![P50、P90、P95 和 P99](images/kibana-latency.png)

![国家、城市、地区和错误请求 IP](images/kibana-geo.png)

| Data View | 索引模式 | 记录类型 |
| --- | --- | --- |
| Nginx | `nginx-access-*` | Nginx 访问日志 |
| Nginx Security | `nginx-security-*` | WAF 安全日志 |
| Applications | `app-logs-*` | 应用日志 |
| Vulnerabilities | `vulnerability-findings-*` | 扫描发现记录 |

[完整索引和安全巡检配置](../elasticsearch-indexes.md)

## 自动巡检与运维

![Google Authenticator 双重验证登录](images/totp-login.png)

![Elasticsearch 只读连接检查](images/source-management.png)

![Nginx 风险接口与 GeoIP](images/nginx-risk.png)

![Nginx 安全巡检规则](images/nginx-inspection.png)

![通过 Run ID 追踪异常](images/incident-investigation.png)

![巡检报告和下载格式](images/inspection-report.png)

![漏洞发现与通知](images/vulnerability-alerts.png)
