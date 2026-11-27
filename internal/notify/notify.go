package notify

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
	"sre-aiobs/internal/store"
)

type Service struct {
	HTTP  *http.Client
	SES   *sesv2.Client
	Store store.Store
}

func (s Service) Telegram(ctx context.Context, c config.Config, r platform.Report, settings platform.AlertSettings) error {
	if c.TelegramChat == "" {
		return nil
	}
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("Telegram credential missing")
	}
	return s.deliverAlert(ctx, "telegram", r, settings, func(ctx context.Context, text string) error {
		return s.postAlert(ctx, "telegram", "https://api.telegram.org/bot"+token+"/sendMessage", map[string]string{"chat_id": c.TelegramChat, "text": text})
	})
}

type latencyRow struct {
	Endpoint      string
	P50, P95, P99 float64
}

type reportView struct {
	platform.Report
	Regions            map[string]int
	Sensitive          int64
	Latency            []latencyRow
	Vulnerabilities    []platform.Event
	VulnerabilityTotal int
}

var htmlReport = template.Must(template.New("report").Parse(`<!doctype html><html><body><h1>日志探测报告</h1><p>{{.Source}} / {{.Index}} / {{.Environment}}</p><p>{{.Window.Start}} 至 {{.Window.End}}</p><p>原始日志量 {{.Snapshot.Total}}，敏感 URI 事件请求量 {{.Sensitive}}</p>{{if .VulnerabilityScan}}<h2>漏洞扫描发现</h2><p>本窗口共 {{.VulnerabilityTotal}} 个 CVE，最多展示 100 项。</p>{{if .Vulnerabilities}}<table><tr><th>CVE</th><th>扫描记录数</th></tr>{{range .Vulnerabilities}}<tr><td>{{.Target}}</td><td>{{.Count}}</td></tr>{{end}}</table>{{else}}<p>本窗口未收到符合条件的 CVE 记录。</p>{{end}}{{end}}{{with .MetricsURL}}<p><a href="{{.}}">查看 VictoriaMetrics 窗口指标</a></p>{{end}}<h2>地区分布</h2><ul>{{range $region,$count := .Regions}}<li>{{$region}} {{$count}}</li>{{end}}</ul><h2>风险事件与异常 IP</h2><table><tr><th>规则</th><th>目标标识</th><th>IP</th><th>国家</th><th>地区</th><th>次数</th><th>风险</th></tr>{{range .Events}}<tr><td>{{.Rule}}</td><td>{{.Target}}</td><td>{{.IP}}</td><td>{{.Country}}</td><td>{{.Region}}</td><td>{{.Count}}</td><td>{{.Score}}</td></tr>{{end}}</table><h2>接口响应时间（秒）</h2><table><tr><th>接口标识</th><th>P50</th><th>P95</th><th>P99</th></tr>{{range .Latency}}<tr><td>{{.Endpoint}}</td><td>{{.P50}}</td><td>{{.P95}}</td><td>{{.P99}}</td></tr>{{end}}</table>{{with .Analysis}}<h2>AI 分析</h2><p>{{.Severity}} {{.Summary}}</p><ul>{{range .Actions}}<li>{{.}}</li>{{end}}</ul>{{end}}</body></html>`))

func (s Service) Email(ctx context.Context, c config.Config, r platform.Report) error {
	if len(c.EmailTo) == 0 {
		return nil
	}
	view := reportView{Report: r, Regions: map[string]int{}}
	for endpoint, quantiles := range r.Snapshot.Latency {
		view.Latency = append(view.Latency, latencyRow{Endpoint: endpoint[:min(16, len(endpoint))], P50: quantiles["50.0"], P95: quantiles["95.0"], P99: quantiles["99.0"]})
	}
	sort.Slice(view.Latency, func(i, j int) bool {
		if view.Latency[i].P95 != view.Latency[j].P95 {
			return view.Latency[i].P95 > view.Latency[j].P95
		}
		return view.Latency[i].Endpoint < view.Latency[j].Endpoint
	})
	if len(view.Latency) > 20 {
		view.Latency = view.Latency[:20]
	}
	unique := map[string]bool{}
	for _, e := range r.Events {
		if e.Rule == platform.VulnerabilityRule {
			view.VulnerabilityTotal++
			if len(view.Vulnerabilities) < 100 {
				view.Vulnerabilities = append(view.Vulnerabilities, e)
			}
		}
		if e.Rule == "sensitive_uri" && e.IP == "" {
			view.Sensitive += e.Count
		}
		if e.IP != "" && !unique[e.IP] {
			view.Regions[e.Country+" "+e.Region]++
			unique[e.IP] = true
		}
	}
	if len(view.Events) > 200 {
		view.Events = view.Events[:200]
	}
	var body bytes.Buffer
	if err := htmlReport.Execute(&body, view); err != nil {
		return err
	}
	subject := "Log detection report " + r.Source
	if r.VulnerabilityScan {
		period := "小时报告"
		if r.Window.End.Sub(r.Window.Start) == 24*time.Hour {
			period = "日报"
		}
		subject = "漏洞检测" + period + " · " + r.Source
	}
	_, err := s.SES.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(c.EmailFrom), Destination: &types.Destination{ToAddresses: c.EmailTo},
		Content: &types.EmailContent{Simple: &types.Message{Subject: &types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{Html: &types.Content{Data: aws.String(body.String()), Charset: aws.String("UTF-8")}}}},
	})
	if err != nil {
		return errors.New("SES send failed")
	}
	return nil
}
