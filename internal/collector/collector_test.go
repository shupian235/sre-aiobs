package collector

import (
	"strings"
	"testing"
)

func TestValidateCollectorSettings(t *testing.T) {
	fixture := func() Settings {
		return Settings{
			User: "deployer", Gateway: "https://logs.internal.example:9443", LogDir: "/var/log/nginx",
			LogGlob: "access*.jsonl", Password: strings.Repeat("e", 48),
			Hosts: []Host{{Name: "edge-01", Address: "192.0.2.10", Port: 22}},
		}
	}
	if err := fixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Settings){
		"duplicate target":   func(s *Settings) { s.Hosts = append(s.Hosts, Host{"edge-02", "192.0.2.10", 22}) },
		"inventory variable": func(s *Settings) { s.Hosts[0].Address = "{{ deployed_secret }}" },
		"gateway path":       func(s *Settings) { s.Gateway += "/custom" },
		"path traversal":     func(s *Settings) { s.LogDir += "/../etc" },
		"compose variable":   func(s *Settings) { s.Password = "${SECRET}" },
		"reserved group":     func(s *Settings) { s.Hosts[0].Name = "all" },
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			change(&s)
			if s.Validate() == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
}
