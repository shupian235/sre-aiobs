package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"sre-aiobs/internal/platform"
	"sre-aiobs/internal/store"
)

var slackPath = regexp.MustCompile(`^/services/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

func slackWebhookValid(webhook string) bool {
	u, err := url.Parse(webhook)
	return err == nil && u.Scheme == "https" && u.Host == "hooks.slack.com" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && slackPath.MatchString(u.EscapedPath())
}

func (s Service) Slack(ctx context.Context, r platform.Report, settings platform.AlertSettings) error {
	webhook := os.Getenv("SLACK_WEBHOOK_URL")
	if !slackWebhookValid(webhook) {
		return errors.New("invalid Slack incoming webhook")
	}
	return s.deliverAlert(ctx, "slack", r, settings, func(ctx context.Context, text string) error {
		if chars := []rune(text); len(chars) > 2900 {
			text = string(chars[:2900]) + "…"
		}
		blocks := []map[string]any{{"type": "section", "text": map[string]string{"type": "plain_text", "text": text}}}
		if r.MetricsURL != "" {
			blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{
				{"type": "button", "text": map[string]string{"type": "plain_text", "text": "查看窗口指标"}, "url": r.MetricsURL},
			}})
		}
		payload := map[string]any{"text": "SRE AIObs 安全告警", "blocks": blocks}
		return s.postAlert(ctx, "slack", webhook, payload)
	})
}

func (s Service) deliverAlert(ctx context.Context, channel string, r platform.Report, settings platform.AlertSettings, send func(context.Context, string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var lines []string
	var releases []func()
	success := false
	defer func() {
		if !success {
			for _, release := range releases {
				release()
			}
		}
	}()
	for _, e := range r.Events {
		if e.Rule == platform.VulnerabilityRule || e.Score < settings.MinimumScore {
			continue
		}
		identity := e.IP
		if strings.HasPrefix(e.Rule, "backend_") {
			identity = e.Target
		}
		key := fmt.Sprintf("%s:%s:%s:%s", channel, r.Source, e.Rule, identity)
		release, ok, err := s.Store.Lock(ctx, key, time.Duration(settings.CooldownMinutes)*time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		releases = append(releases, release)
		line := fmt.Sprintf("%s IP=%s %s %s score=%d count=%d", e.Rule, e.IP, e.Country, e.Region, e.Score, e.Count)
		if strings.HasPrefix(e.Rule, "backend_") {
			line = fmt.Sprintf("%s score=%d count=%d", e.Rule, e.Score, e.Count)
			if e.Target != "" {
				line += " target=" + e.Target
			}
		}
		lines = append(lines, line)
		if len(lines) == 20 {
			break
		}
	}
	if len(lines) == 0 {
		return store.ErrDeduplicated
	}
	message := fmt.Sprintf("%s (%s)\n%s", r.Source, r.Environment, strings.Join(lines, "\n"))
	if channel != "slack" && r.MetricsURL != "" {
		message += "\n窗口指标: " + r.MetricsURL
	}
	err := send(ctx, message)
	success = err == nil
	return err
}

func (s Service) postAlert(ctx context.Context, channel, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid %s request", channel)
	}
	req.Header.Set("Content-Type", "application/json")
	client := *s.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s transport failed", channel)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s HTTP status %d", channel, resp.StatusCode)
	}
	return nil
}
