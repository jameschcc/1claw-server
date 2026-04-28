package config

import (
	"fmt"
	"os"

	"1claw-server/internal/model"

	"gopkg.in/yaml.v3"
)

// Load reads and parses the YAML config file.
func Load(path string) (*model.ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &model.ServerConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.WSPath == "" {
		cfg.Server.WSPath = "/ws"
	}
	if cfg.Server.APIPath == "" {
		cfg.Server.APIPath = "/api"
	}
	if cfg.WebSocket.PingInterval == 0 {
		cfg.WebSocket.PingInterval = 30
	}
	if cfg.WebSocket.WriteTimeout == 0 {
		cfg.WebSocket.WriteTimeout = 10
	}
	if cfg.WebSocket.ReadTimeout == 0 {
		cfg.WebSocket.ReadTimeout = 60
	}
	if cfg.WebSocket.MaxMessageSize == 0 {
		cfg.WebSocket.MaxMessageSize = 65536
	}

	return cfg, nil
}
