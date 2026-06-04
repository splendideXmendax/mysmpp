package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Server    ServerConfig     `json:"server"`
	SMPP      SMPPConfig       `json:"smpp"`
	ESMEs     []ESMECred       `json:"esmes"`
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

type ESMECred struct {
	SystemID string `json:"system_id"`
	Password string `json:"password"`
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

func (c *Config) Normalize() {
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":8080"
	}
	if c.Server.ShutdownTimeout == "" {
		c.Server.ShutdownTimeout = "10s"
	}
	if c.SMPP.Addr == "" {
		c.SMPP.Addr = ":2775"
	}
	if c.Storage.Driver == "" {
		c.Storage.Driver = "memory"
	}
	for i := range c.Inbound {
		if c.Inbound[i].Method == "" {
			c.Inbound[i].Method = "POST"
		}
		if c.Inbound[i].SuccessStatus == 0 {
			c.Inbound[i].SuccessStatus = 200
		}
	}
	for i := range c.Outbound {
		if c.Outbound[i].Method == "" {
			c.Outbound[i].Method = "POST"
		}
		if c.Outbound[i].ContentType == "" {
			c.Outbound[i].ContentType = "application/x-www-form-urlencoded"
		}
	}
}

func (c Config) Validate() error {
	names := map[string]string{}
	for _, provider := range c.Providers {
		if provider.Name == "" {
			return fmt.Errorf("provider name is required")
		}
		if _, ok := names["provider:"+provider.Name]; ok {
			return fmt.Errorf("duplicate provider %q", provider.Name)
		}
		names["provider:"+provider.Name] = provider.Name
	}
	for _, route := range c.Routes {
		if route.Name == "" {
			return fmt.Errorf("route name is required")
		}
		if route.Provider == "" {
			return fmt.Errorf("route %q provider is required", route.Name)
		}
	}
	esmeNames := map[string]struct{}{}
	for _, esme := range c.ESMEs {
		if esme.SystemID == "" {
			return fmt.Errorf("esme system_id is required")
		}
		if _, ok := esmeNames[esme.SystemID]; ok {
			return fmt.Errorf("duplicate esme %q", esme.SystemID)
		}
		esmeNames[esme.SystemID] = struct{}{}
	}
	for _, rule := range c.Inbound {
		if rule.Name == "" {
			return fmt.Errorf("inbound rule name is required")
		}
		if rule.Path == "" || !strings.HasPrefix(rule.Path, "/") {
			return fmt.Errorf("inbound rule %q path must start with /", rule.Name)
		}
		if rule.Fields["from"] == "" || rule.Fields["to"] == "" || rule.Fields["text"] == "" {
			return fmt.Errorf("inbound rule %q must map from, to, and text", rule.Name)
		}
	}
	for _, rule := range c.Outbound {
		if rule.Name == "" {
			return fmt.Errorf("outbound rule name is required")
		}
		if !mapsTo(rule.Fields, "to") || !mapsTo(rule.Fields, "text") {
			return fmt.Errorf("outbound rule %q must map to and text", rule.Name)
		}
	}
	return nil
}

func mapsTo(fields map[string]string, internal string) bool {
	for _, source := range fields {
		if source == internal {
			return true
		}
	}
	return false
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
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}
