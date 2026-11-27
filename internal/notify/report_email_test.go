package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"sre-aiobs/internal/platform"
)

func TestInspectionReportsUseSESHTML(t *testing.T) {
	checked := int64(3)
	for _, item := range []struct {
		name, title, content string
		period               time.Duration
		report               platform.Report
	}{
		{"security", "Nginx 安全巡检小时报告", "nginx_waf_block", time.Hour, platform.Report{
			NginxSecurity: true, Events: []platform.Event{{Rule: "nginx_waf_block", Score: 85, IP: "192.0.2.2", Target: "<unsafe>"}},
		}},
		{"vulnerability", "漏洞巡检日报", "CVE-2026-0001", 24 * time.Hour, platform.Report{
			VulnerabilityScan: true, Events: []platform.Event{{Rule: platform.VulnerabilityRule, Target: "CVE-2026-0001", Count: 3}},
		}},
		{"aws", "AWS 云端巡检小时报告", "检查 3 · 正常 2 · 告警 1 · 采样不足 0", time.Hour, platform.Report{
			AWSRegion: "ap-southeast-1",
			Snapshot:  platform.Snapshot{CloudAlarmCount: &checked},
			Events:    []platform.Event{{Rule: platform.CloudAlarmRule, Score: 90, Target: "cpu"}},
		}},
		{"empty-security", "Nginx 安全巡检小时报告", "本窗口没有命中安全规则", time.Hour, platform.Report{NginxSecurity: true}},
	} {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			client := sesv2.NewFromConfig(aws.Config{
				Region: "us-east-1",
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
				}),
				HTTPClient: &http.Client{Transport: alertTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					var sent sesv2.SendEmailInput
					err := json.NewDecoder(req.Body).Decode(&sent)
					if err != nil || sent.Content == nil || sent.Content.Simple == nil || sent.Content.Simple.Body == nil || sent.Content.Simple.Body.Html == nil {
						t.Fatalf("expected an SES HTML email: %v", err)
					}
					if req.URL.Host != "email.ap-southeast-1.amazonaws.com" ||
						aws.ToString(sent.FromEmailAddress) != "security@example.net" ||
						sent.Destination == nil || strings.Join(sent.Destination.ToAddresses, ",") != "ops@example.net" ||
						!strings.Contains(aws.ToString(sent.Content.Simple.Subject.Data), item.title) {
						t.Fatalf("unexpected envelope or subject: %+v", sent)
					}
					body := aws.ToString(sent.Content.Simple.Body.Html.Data)
					if !strings.Contains(body, item.content) || !strings.Contains(body, "fixture-run") || strings.Contains(body, "<unsafe>") {
						t.Fatalf("unexpected HTML: %s", body)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"MessageId":"test"}`))}, nil
				})},
			})
			r := item.report
			r.RunID = "fixture-run"
			r.Window.End = r.Window.Start.Add(item.period)
			err := (Service{SES: client}).Email(context.Background(), platform.EmailDestination{
				From: "security@example.net", To: []string{"ops@example.net"}, Region: "ap-southeast-1",
			}, r)
			if err != nil || calls != 1 {
				t.Fatalf("expected one simulated delivery: %v, calls=%d", err, calls)
			}
		})
	}
}
