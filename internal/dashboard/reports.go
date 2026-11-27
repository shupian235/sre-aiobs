package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/store"
)

type reportStore interface {
	RunReport(context.Context, string) (store.RunReport, error)
}

var reportPage = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><title>巡检报告 · {{.ID}}</title><link rel="stylesheet" href="/app.css">
<main class="content"><p><a href="/#tasks">返回任务记录</a> · <a href="?format=html">下载 HTML</a> · <a href="?format=json">下载 JSON</a> · <a href="?format=markdown">下载 Markdown</a></p>
<h1>巡检报告</h1>
<dl><dt>Run ID</dt><dd>{{.ID}}</dd><dt>Correlation ID</dt><dd>{{.CorrelationID}}</dd>
<dt>来源与任务</dt><dd>{{.Source}} · {{.Kind}}</dd><dt>状态与尝试次数</dt><dd>{{.Status}} · {{.Attempts}}</dd>
<dt>窗口</dt><dd>{{.Start}} 至 {{.End}}</dd><dt>最近更新时间</dt><dd>{{.Updated}}</dd>
<dt>原始日志量</dt><dd>{{if .Total}}{{.Total}}{{else}}暂无采集结果{{end}}</dd></dl>
<h2>延迟分位数（秒）</h2><ul>{{range $k,$v := .Latency}}<li>P{{$k}} · {{$v}}</li>{{else}}<li>当前窗口没有有效延迟值</li>{{end}}</ul>
<h2>事件统计</h2><ul>{{range $k,$v := .ByRule}}<li>{{$k}} · {{$v}}</li>{{else}}<li>暂无事件记录</li>{{end}}</ul>
<h2>投递状态</h2><ul>{{range $k,$v := .Delivery}}<li>{{$k}} · {{$v}}</li>{{else}}<li>暂无投递记录</li>{{end}}</ul>
<h2>AI 分析</h2>{{with .Analysis}}<p>{{.Severity}} · {{.Summary}}</p><ul>{{range .Actions}}<li>{{.}}</li>{{end}}</ul>{{else}}<p>暂无分析结果。</p>{{end}}</main></html>`))

func mountReports(mux *http.ServeMux, backend reportStore, protect func(http.Handler) http.HandlerFunc) {
	mux.HandleFunc("GET /api/reports/{run}", protect(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("run")
		if !runIDPattern.MatchString(id) {
			http.NotFound(w, req)
			return
		}
		format := req.URL.Query().Get("format")
		if format != "" && format != "html" && format != "json" && format != "markdown" {
			http.Error(w, "Unknown report format", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 4*time.Second)
		defer cancel()
		report, err := backend.RunReport(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, req)
			return
		}
		if err != nil {
			http.Error(w, "Report unavailable", http.StatusServiceUnavailable)
			return
		}

		switch format {
		case "json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="report-`+id+`.json"`)
			_ = json.NewEncoder(w).Encode(report)
		case "markdown":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="report-`+id+`.md"`)
			_, _ = fmt.Fprintf(w, "# 巡检报告\n\n- Run ID: %s\n- 数据源: %s\n- 状态: %s\n- 窗口: %s 至 %s\n", id, report.Source, report.Status, report.Start.UTC().Format(time.RFC3339), report.End.UTC().Format(time.RFC3339))
			if report.Total == nil {
				_, _ = fmt.Fprintln(w, "- 日志量: 暂无采集结果")
			} else {
				_, _ = fmt.Fprintf(w, "- 日志量: %d\n", *report.Total)
			}
			// Indenting the serialized details keeps stored text literal in Markdown renderers.
			payload, _ := json.MarshalIndent(report, "", "  ")
			_, _ = fmt.Fprintf(w, "\n## 详细数据\n\n    %s\n", strings.ReplaceAll(string(payload), "\n", "\n    "))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if format == "html" {
				w.Header().Set("Content-Disposition", `attachment; filename="report-`+id+`.html"`)
			}
			_ = reportPage.Execute(w, report)
		}
	})))
}
