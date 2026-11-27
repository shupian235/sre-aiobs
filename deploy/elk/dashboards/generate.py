#!/usr/bin/env python3
"""Generate Kibana 8.17 Saved Objects from the bundled Elasticsearch fields."""
import json
from pathlib import Path

DATA_VIEW = "bc589ecf-5117-40ca-b632-cb021652f611"
TRAFFIC_FILTER = 'NOT request_path : "/ws/v1/" AND NOT service : "cb" AND NOT user_agent : "ELB-HealthChecker/2.0"'
ERROR_4XX = "status >= 400 AND status < 500"
ERROR_5XX = "status >= 500 AND status < 600"


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def metric(kind="count", field=None, **params):
    if field:
        params["field"] = field
    return {"type": kind, "schema": "metric", "params": params}


def terms(field, size=15, schema="bucket"):
    return {"type": "terms", "schema": schema, "params": {
        "field": field, "size": size, "order": "desc", "orderBy": "1",
    }}


def histogram():
    return {"type": "date_histogram", "schema": "segment", "params": {
        "field": "@timestamp", "interval": "auto", "min_doc_count": 0,
        "extended_bounds": {},
    }}


def search_source(query):
    return encode({"query": {"query": query, "language": "kuery"}, "filter": []})


def qps_spec():
    return {
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        "description": "每个一分钟桶的请求数除以 60；范围首尾可能为部分分钟。",
        "data": {
            "url": {"%context%": True, "%timefield%": "@timestamp", "index": "nginx-access-*",
                    "body": {"size": 0, "aggs": {"minutes": {"date_histogram": {
                        "field": "@timestamp", "fixed_interval": "1m", "min_doc_count": 0,
                    }}}}},
            "format": {"property": "aggregations.minutes.buckets"},
        },
        "transform": [{"calculate": "datum.doc_count / 60", "as": "qps"}],
        "mark": {"type": "line", "tooltip": True},
        "encoding": {
            "x": {"field": "key", "type": "temporal", "title": "时间"},
            "y": {"field": "qps", "type": "quantitative", "title": "请求数 / 秒"},
        },
    }


class Dashboard:
    def __init__(self, slug, title, query="", data_view=DATA_VIEW):
        self.data_view = data_view
        self.id = f"sre-aiobs-nginx-{slug}"
        self.title = title
        self.query = query
        self.panels = []
        self.objects = []
        self.references = []
        self.x = 0
        self.y = 0
        self.row_height = 0

    def panel(self, slug, title, kind, aggs=(), query="", width=24):
        panel_id = f"{self.id}-{slug}"
        height = 6 if kind == "metric" else 12
        if self.x + width > 48:
            self.y += self.row_height
            self.x = self.row_height = 0
        if kind == "vega":
            params = {"spec": encode(qps_spec())}
        elif kind == "table":
            params = {"perPage": 10, "showPartialRows": False, "showMetricsAtAllLevels": False}
        else:
            params = {}
        if kind == "line":
            params = {
                "addTooltip": True, "addLegend": True, "legendPosition": "right",
                "categoryAxes": [{"id": "CategoryAxis-1", "type": "category", "position": "bottom", "scale": {"type": "linear"}, "labels": {"show": True}}],
                "valueAxes": [{"id": "ValueAxis-1", "type": "value", "position": "left", "scale": {"type": "linear"}, "labels": {"show": True}}],
                "seriesParams": [{"data": {"id": str(i), "label": agg["type"]}, "drawLinesBetweenPoints": True, "mode": "normal", "show": True, "type": "line", "valueAxis": "ValueAxis-1"}
                                 for i, agg in enumerate(aggs, 1) if agg["schema"] == "metric"],
            }
        source = {"query": {"query": query, "language": "kuery"}, "filter": []}
        references = []
        if kind != "vega":
            reference = "kibanaSavedObjectMeta.searchSourceJSON.index"
            source["indexRefName"] = reference
            references.append({"name": reference, "type": "index-pattern", "id": self.data_view})
        vis_state = {"title": title, "type": kind, "params": params, "aggs": [
            dict(agg, id=str(i), enabled=True) for i, agg in enumerate(aggs, 1)
        ]}
        self.objects.append({
            "id": panel_id, "type": "visualization", "references": references,
            "attributes": {"title": title, "description": "", "uiStateJSON": "{}",
                           "visState": encode(vis_state),
                           "kibanaSavedObjectMeta": {"searchSourceJSON": encode(source)}},
        })
        idx = str(len(self.panels) + 1)
        ref = f"panel_{idx}"
        self.references.append({"name": ref, "type": "visualization", "id": panel_id})
        self.panels.append({"panelIndex": idx, "panelRefName": ref, "type": "visualization",
                            "embeddableConfig": {}, "version": "8.17.0",
                            "gridData": {"x": self.x, "y": self.y, "w": width, "h": height, "i": idx}})
        self.x += width
        self.row_height = max(height, self.row_height)

    def write(self):
        dashboard = {"id": self.id, "type": "dashboard", "references": self.references,
                     "attributes": {"title": self.title, "description": "sre-aiobs Elasticsearch 日志看板",
                                    "panelsJSON": encode(self.panels), "optionsJSON": encode({"useMargins": True, "hidePanelTitles": False}),
                                    "timeRestore": False, "version": 1,
                                    "kibanaSavedObjectMeta": {"searchSourceJSON": search_source(self.query)}}}
        path = Path(__file__).with_name(self.id + ".ndjson")
        path.write_text("\n".join(map(encode, [*self.objects, dashboard])) + "\n", encoding="utf-8")


def main():
    traffic = Dashboard("access", "Nginx Access 日志看板", TRAFFIC_FILTER)
    count = metric()
    avg_latency = metric("avg", "request_time")
    p99 = metric("percentiles", "request_time", percents=[99])
    p_all = metric("percentiles", "request_time", percents=[50, 90, 95, 99])
    error = metric("avg", "error_rate_sample", customLabel="错误率 (%)")
    status_counts = (count, metric("sum", "status_4xx_sample", customLabel="4xx"),
                     metric("sum", "status_5xx_sample", customLabel="5xx"), error)
    traffic.panel("requests", "总请求量", "metric", (count,), width=12)
    traffic.panel("errors", "错误率 (%)", "metric", (error,), width=12)
    traffic.panel("avg-latency", "平均响应时间 (s)", "metric", (avg_latency,), width=12)
    traffic.panel("p99", "2xx 响应时间 P99 (s)", "metric", (p99,), "status >= 200 AND status < 300", width=12)
    traffic.panel("request-trend", "请求量趋势", "line", (count, histogram()))
    traffic.panel("qps", "每分钟平均 QPS / RPS", "vega")
    traffic.panel("percentiles", "响应时间 P50 / P90 / P95 / P99 (s)", "line", (p_all, histogram()), width=48)
    traffic.panel("host-trend", "按域名请求趋势", "line", (count, histogram(), terms("nginx_host", 10, "group")))
    traffic.panel("service-trend", "按服务请求趋势", "line", (count, histogram(), terms("service", 10, "group")))
    traffic.panel("service-p99", "按服务 P99 (s)", "line", (p99, histogram(), terms("service", 10, "group")))
    traffic.panel("node-trend", "按采集主机请求趋势", "line", (count, histogram(), terms("log_hostname", 10, "group")))
    traffic.panel("error-trend", "4xx / 499 / 5xx 趋势", "line", (count, histogram(), terms("status", 25, "group")), f"({ERROR_4XX}) OR ({ERROR_5XX})")
    traffic.panel("unusual-ua", "可疑 User-Agent 趋势", "line", (count, histogram()), 'NOT user_agent : * OR user_agent : "" OR user_agent : curl* OR user_agent : wget* OR user_agent : python-requests*')
    for slug, title, field, n in [
        ("status", "状态码分布", "status", 20), ("method", "HTTP 方法分布", "method", 10),
        ("routes", "Top 15 请求接口", "request_path", 15), ("ua", "Top 10 User-Agent", "user_agent", 10),
        ("domains", "Top 10 域名", "nginx_host", 10), ("ips", "Top 20 Client IP", "client_ip", 20),
        ("countries", "来源国家", "client_geo.country_name", 20), ("regions", "来源地区", "client_geo.region_iso_code", 20),
    ]:
        traffic.panel(slug, title, "table", (count, terms(field, n)))
    traffic.panel("slow", "接口响应时间分析 (s)", "table", (count, avg_latency, metric("max", "request_time"), metric("percentiles", "request_time", percents=[95]), terms("request_path")), "request_time >= 0.001")
    traffic.panel("error-routes", "出错接口排行", "table", (count, terms("request_path")), f"({ERROR_4XX}) OR ({ERROR_5XX})")
    traffic.panel("ip-errors", "高频 IP 与错误状态", "table", (*status_counts, terms("client_ip", 20)))
    traffic.panel("host-errors", "域名错误率", "table", (*status_counts, terms("nginx_host", 20)))
    traffic.panel("service-errors", "服务错误率", "table", (*status_counts, terms("service", 20)))
    traffic.panel("route-host", "域名 × 接口请求量", "table", (count, terms("nginx_host", 10), terms("request_path", 5)))
    traffic.panel("egress", "响应体流量 (bytes)", "line", (metric("sum", "body_bytes_sent"), histogram()), width=48)
    traffic.write()

    geo = Dashboard("geo", "Nginx 地理分布看板")
    country = "client_geo.country_name"
    region = "client_geo.region_iso_code"
    geo.panel("requests", "总请求量", "metric", (count,), width=12)
    geo.panel("4xx", "4xx 请求数", "metric", (count,), ERROR_4XX, width=12)
    geo.panel("5xx", "5xx 请求数", "metric", (count,), ERROR_5XX, width=12)
    geo.panel("country-count", "来源国家数（估算）", "metric", (metric("cardinality", "client_geo.country_iso_code"),), width=12)
    geo.panel("top-country", "Top 20 来源国家", "table", (count, terms(country, 20)))
    geo.panel("top-city", "Top 20 城市", "table", (count, terms("client_geo.city_name", 20)))
    geo.panel("country-status", "状态码 × 来源国家", "table", (*status_counts, terms(country, 20)), width=48)
    geo.panel("country-trend", "流量趋势（Top 5 国家）", "line", (count, histogram(), terms(country, 5, "group")))
    geo.panel("country-error-trend", "错误趋势（Top 5 国家）", "line", (count, histogram(), terms(country, 5, "group")), f"({ERROR_4XX}) OR ({ERROR_5XX})")
    geo.panel("ip-country", "高频 IP × 来源国家", "table", (*status_counts, terms("client_ip", 20), terms(country, 5)), width=48)
    geo.panel("region-status", "状态码 × 来源地区", "table", (*status_counts, terms(region, 20)), width=48)
    geo.write()

    security = Dashboard("security", "Nginx 安全巡检日志看板", data_view="sre-aiobs-nginx-security")
    security.panel("total", "安全日志总数", "metric", (count,))
    security.panel("blocks", "WAF 拦截日志数", "metric", (count,), 'waf_action:("block" OR "blocked" OR "deny" OR "denied" OR "drop" OR "dropped" OR "reject" OR "rejected")')
    security.panel("trend", "安全日志趋势", "line", (count, histogram()), width=48)
    security.panel("actions", "WAF 动作分布", "table", (count, terms("waf_action", 10)))
    security.panel("categories", "WAF 安全分类", "table", (count, terms("waf_category", 20)))
    security.panel("ips", "客户端 IP 排行", "table", (count, terms("client_ip", 20)))
    security.panel("regions", "客户端地区排行", "table", (count, terms("client_geo.region_iso_code", 20)))
    security.write()



if __name__ == "__main__":
    main()
