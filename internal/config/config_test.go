package config

import (
	"testing"
	"os"
)

func TestLoad_Defaults(t *testing.T) {
	// Write a minimal config
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	content := []byte("server:\n  host: \"127.0.0.1\"\nprofiles:\n  - id: test\n    name: Test\n    emoji: \"🧪\"\n")
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.WebSocket.PingInterval != 30 {
		t.Errorf("expected default ping_interval 30, got %d", cfg.WebSocket.PingInterval)
	}
	if len(cfg.Profiles) != 1 {
		t.Errorf("expected 1 profile, got %d", len(cfg.Profiles))
	}
	if cfg.Profiles[0].ID != "test" {
		t.Errorf("expected profile id 'test', got %s", cfg.Profiles[0].ID)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
