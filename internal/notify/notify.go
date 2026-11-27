package notify

import (
	"bytes"
	"context"
	_ "embed"
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
	Regions                     map[string]int
	Sensitive                   int64
	Latency                     []latencyRow
	Vulnerabilities             []platform.Event
	VulnerabilityTotal          int
	SecurityEvents              []platform.Event
	SecurityTotal, SecurityHigh int
	Title                       string
}

//go:embed report-email.html
var emailTemplate string

var htmlReport = template.Must(template.New("report").Parse(emailTemplate))

func (s Service) Email(ctx context.Context, destination platform.EmailDestination, r platform.Report) error {
	if !destination.Configured() {
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
	securityRules := map[string]bool{}
	for _, rule := range platform.NginxRules() {
		securityRules[rule] = true
	}
	view.Events = nil
	for _, e := range r.Events {
		switch {
		case e.Rule == platform.VulnerabilityRule:
			view.VulnerabilityTotal++
			if len(view.Vulnerabilities) < 100 {
				view.Vulnerabilities = append(view.Vulnerabilities, e)
			}
		case securityRules[e.Rule]:
			view.SecurityTotal++
			if e.Score >= 80 {
				view.SecurityHigh++
			}
			if len(view.SecurityEvents) < 100 {
				view.SecurityEvents = append(view.SecurityEvents, e)
			}
		default:
			view.Events = append(view.Events, e)
		}
		if e.Rule == "sensitive_uri" && e.IP == "" {
			view.Sensitive += e.Count
		}
		if e.IP != "" && !unique[e.IP] {
			region := e.Country + " " + e.Region
			if region == " " {
				region = "未知地区"
			}
			view.Regions[region]++
			unique[e.IP] = true
		}
	}
	view.NginxSecurity = view.NginxSecurity || view.SecurityTotal > 0
	view.VulnerabilityScan = view.VulnerabilityScan || view.VulnerabilityTotal > 0
	period := "小时报告"
	if r.Window.End.Sub(r.Window.Start) == 24*time.Hour {
		period = "日报"
	}
	switch {
	case view.NginxSecurity && view.VulnerabilityScan:
		view.Title = "安全与漏洞巡检" + period
	case view.VulnerabilityScan:
		view.Title = "漏洞巡检" + period
	case view.NginxSecurity:
		view.Title = "Nginx 安全巡检" + period
	default:
		view.Title = "日志探测" + period
	}
	if len(view.Events) > 200 {
		view.Events = view.Events[:200]
	}
	var body bytes.Buffer
	if err := htmlReport.Execute(&body, view); err != nil {
		return err
	}
	subject := view.Title + " · " + r.Source
	_, err := s.SES.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(destination.From), Destination: &types.Destination{ToAddresses: destination.To},
		Content: &types.EmailContent{Simple: &types.Message{Subject: &types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{Html: &types.Content{Data: aws.String(body.String()), Charset: aws.String("UTF-8")}}}},
	}, func(options *sesv2.Options) {
		if destination.Region != "" {
			options.Region = destination.Region
		}
	})
	if err != nil {
		return errors.New("SES send failed")
	}
	return nil
}
