package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmptySourcesRequireExplicitOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("sources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted an empty sources list without opt-in")
	}
	if err := os.WriteFile(path, []byte("allow_empty_sources: true\nsources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || !c.AllowEmptySources || len(c.Sources) != 0 {
		t.Fatalf("unexpected starter configuration: %+v %v", c, err)
	}
}
