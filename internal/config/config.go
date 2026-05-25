package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Server    ServerConfig     `json:"server"`
	SMPP      SMPPConfig       `json:"smpp"`
	Routes    []RouteConfig    `json:"routes"`
	Providers []ProviderConfig `json:"providers"`
	Inbound   []HTTPRuleConfig `json:"inbound"`
	Outbound  []HTTPRuleConfig `json:"outbound"`
	Storage   StorageConfig    `json:"storage"`
}

type ServerConfig struct {
	HTTPAddr        string `json:"http_addr"`
	ShutdownTimeout string `json:"shutdown_timeout"`
}

type SMPPConfig struct {
	Addr          string `json:"addr"`
	SystemID      string `json:"system_id"`
	Password      string `json:"password"`
	SystemType    string `json:"system_type"`
	MaxSessions   int    `json:"max_sessions"`
	WindowSize    int    `json:"window_size"`
	EnquirePeriod string `json:"enquire_period"`
}

type RouteConfig struct {
	Name     string   `json:"name"`
	Prefix   []string `json:"prefix"`
	Provider string   `json:"provider"`
	Priority int      `json:"priority"`
}

type ProviderConfig struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Endpoint string `json:"endpoint"`
	Rule     string `json:"rule"`
	SystemID string `json:"system_id"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

type HTTPRuleConfig struct {
	Name          string            `json:"name"`
	Method        string            `json:"method"`
	Path          string            `json:"path"`
	ContentType   string            `json:"content_type"`
	AuthHeader    string            `json:"auth_header"`
	AuthToken     string            `json:"auth_token"`
	Fields        map[string]string `json:"fields"`
	Headers       map[string]string `json:"headers"`
	SuccessStatus int               `json:"success_status"`
	SuccessBody   string            `json:"success_body"`
}

type StorageConfig struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{HTTPAddr: ":8080", ShutdownTimeout: "10s"},
		SMPP: SMPPConfig{
			Addr:          ":2775",
			SystemID:      "mysmpp",
			Password:      "secret",
			SystemType:    "gateway",
			MaxSessions:   128,
			WindowSize:    16,
			EnquirePeriod: "30s",
		},
		Storage: StorageConfig{Driver: "memory"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Server.HTTPAddr == "" {
		cfg.Server.HTTPAddr = ":8080"
	}
	if cfg.SMPP.Addr == "" {
		cfg.SMPP.Addr = ":2775"
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = "memory"
	}
	return cfg, nil
}
