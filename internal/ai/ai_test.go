package ai

import (
	"sre-aiobs/internal/platform"
	"testing"
)

func TestSchemaAndRedaction(t *testing.T) {
	if _, err := validate(`{"summary":"traffic spike","severity":"high","actions":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := validate(`{"summary":"traffic spike","severity":"high","actions":[],"secret":"x"}`); err == nil {
		t.Fatal("unexpected field accepted")
	}
	e := sanitize([]platform.Event{{Rule: "correlated", Score: 100, IP: "192.0.2.5", Country: "CN", Region: "token=secret", Signals: []string{"prompt"}}})
	if len(e) != 1 || e[0].IP != "192.0.2.5" || e[0].Region != "" || e[0].Signals != nil {
		t.Fatalf("unsafe output %#v", e)
	}
}
