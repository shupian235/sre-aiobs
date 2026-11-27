package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type alertTransport func(*http.Request) (*http.Response, error)

func (t alertTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func TestSlackWebhookAndPost(t *testing.T) {
	const webhook = "https://hooks.slack.com/services/TEAM/CHANNEL/fixture-secret"
	for _, malformed := range []string{"http://hooks.slack.com/services/a/b/c", "https://hooks.slack.com.example.org/services/a/b/c", webhook + "?x=1", webhook + "/", "https://user@hooks.slack.com/services/a/b/c"} {
		if slackWebhookValid(malformed) {
			t.Errorf("accepted %s", malformed)
		}
	}
	if !slackWebhookValid(webhook) {
		t.Fatal("valid webhook rejected")
	}
	service := Service{HTTP: &http.Client{Transport: alertTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != webhook || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["text"] != "fixture message" {
			t.Errorf("invalid payload: %v", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}}
	if err := service.postAlert(context.Background(), "slack", webhook, map[string]string{"text": "fixture message"}); err != nil {
		t.Fatal(err)
	}
	service.HTTP.Transport = alertTransport(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	if err := service.postAlert(context.Background(), "slack", webhook, nil); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
