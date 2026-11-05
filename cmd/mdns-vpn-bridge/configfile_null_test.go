package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRejectsNullServiceBeforeOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"services":["_http",null]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadFileConfig(path)
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("expected null service schema error before any CLI override, got %v", err)
	}
}
