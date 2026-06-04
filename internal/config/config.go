package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const PlaceholderSecret = "CHANGE_ME_BEFORE_DEPLOY"

type Config struct {
	Server    ServerConfig     `json:"server"`
	SMPP      SMPPConfig       `json:"smpp"`
	ESMEs     []ESMECred       `json:"esmes"`
	Routes    []RouteConfig    `json:"routes"`
	Providers []ProviderConfig `json:"providers"`
	Inbound   []HTTPRuleConfig `json:"inbound"`
	Outbound  []HTTPRuleConfig `json:"outbound"`
	Clients   []ClientAuth     `json:"clients"`
	Risk      RiskConfig       `json:"risk"`
	Storage   StorageConfig    `json:"storage"`
	Admin     AdminConfig      `json:"admin"`
}

type ServerConfig struct {
	HTTPAddr        string `json:"http_addr"`
	ShutdownTimeout string `json:"shutdown_timeout"`
}

type SMPPConfig struct {
	Addr                   string `json:"addr"`
	SystemID               string `json:"system_id"`
	Password               string `json:"password"`
	SystemType             string `json:"system_type"`
	MaxSessions            int    `json:"max_sessions"`
	MaxSessionsPerSystemID int    `json:"max_sessions_per_system_id"`
	WindowSize             int    `json:"window_size"`
	EnquirePeriod          string `json:"enquire_period"`
}

type ESMECred struct {
	SystemID string `json:"system_id"`
	Password string `json:"password"`
}

type ClientAuth struct {
	ClientID   string   `json:"client_id"`
	Token      string   `json:"token"`
	Enabled    bool     `json:"enabled"`
	AllowedIPs []string `json:"allowed_ips"`
}

type RiskConfig struct {
	BlockedToPrefix    []string `json:"blocked_to_prefix"`
	BlockedKeywords    []string `json:"blocked_keywords"`
	PerNumberPerMinute int      `json:"per_number_per_minute"`
	PerNumberPerDay    int      `json:"per_number_per_day"`
	PerClientPerSecond int      `json:"per_client_per_second"`
}

type RouteConfig struct {
	Name     string   `json:"name"`
	Prefix   []string `json:"prefix"`
	Provider string   `json:"provider"`
	Priority int      `json:"priority"`
}

type ProviderConfig struct {
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Endpoint  string            `json:"endpoint"`
	Rule      string            `json:"rule"`
	SystemID  string            `json:"system_id"`
	Password  string            `json:"password"`
	Enabled   bool              `json:"enabled"`
	RateLimit ProviderRateLimit `json:"rate_limit"`
}

type ProviderRateLimit struct {
	TPS       int `json:"tps"`
	Burst     int `json:"burst"`
	TimeoutMS int `json:"timeout_ms"`
}

type HTTPRuleConfig struct {
	Name          string              `json:"name"`
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Provider      string              `json:"provider"`
	ContentType   string              `json:"content_type"`
	AuthHeader    string              `json:"auth_header"`
	AuthToken     string              `json:"auth_token"`
	Fields        map[string]string   `json:"fields"`
	Headers       map[string]string   `json:"headers"`
	SuccessStatus int                 `json:"success_status"`
	SuccessBody   string              `json:"success_body"`
	Response      ResponseParseConfig `json:"response"`
}

type ResponseParseConfig struct {
	IDPath  string `json:"id_path"`
	IDRegex string `json:"id_regex"`
}

type StorageConfig struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

type AdminConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{HTTPAddr: ":8080", ShutdownTimeout: "10s"},
		SMPP: SMPPConfig{
			Addr:                   ":2775",
			SystemID:               "mysmpp",
			Password:               "secret",
			SystemType:             "gateway",
			MaxSessions:            128,
			MaxSessionsPerSystemID: 4,
			WindowSize:             16,
			EnquirePeriod:          "30s",
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
	if c.SMPP.MaxSessionsPerSystemID == 0 {
		c.SMPP.MaxSessionsPerSystemID = 4
	}
	if c.SMPP.WindowSize == 0 {
		c.SMPP.WindowSize = 16
	}
	if c.Risk.PerNumberPerMinute == 0 {
		c.Risk.PerNumberPerMinute = 5
	}
	if c.Risk.PerNumberPerDay == 0 {
		c.Risk.PerNumberPerDay = 20
	}
	if c.Risk.PerClientPerSecond == 0 {
		c.Risk.PerClientPerSecond = 100
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
	if c.Admin.Username == "" || c.Admin.Password == "" {
		return fmt.Errorf("admin username and password are required")
	}
	if isPlaceholder(c.Admin.Username) || isPlaceholder(c.Admin.Password) {
		return fmt.Errorf("admin credentials must be changed before deploy")
	}
	if isPlaceholder(c.SMPP.Password) {
		return fmt.Errorf("smpp password must be changed before deploy")
	}
	names := map[string]string{}
	enabledProviders := map[string]bool{}
	for _, provider := range c.Providers {
		if provider.Name == "" {
			return fmt.Errorf("provider name is required")
		}
		if _, ok := names["provider:"+provider.Name]; ok {
			return fmt.Errorf("duplicate provider %q", provider.Name)
		}
		names["provider:"+provider.Name] = provider.Name
		if provider.Enabled {
			enabledProviders[provider.Name] = true
		}
		if provider.RateLimit.TPS < 0 || provider.RateLimit.Burst < 0 || provider.RateLimit.TimeoutMS < 0 {
			return fmt.Errorf("provider %q rate_limit values must be non-negative", provider.Name)
		}
	}
	if len(c.Providers) > 0 && len(enabledProviders) == 0 {
		return fmt.Errorf("at least one provider must be enabled")
	}
	for _, route := range c.Routes {
		if route.Name == "" {
			return fmt.Errorf("route name is required")
		}
		if route.Provider == "" {
			return fmt.Errorf("route %q provider is required", route.Name)
		}
		if _, ok := names["provider:"+route.Provider]; !ok {
			return fmt.Errorf("route %q references unknown provider %q", route.Name, route.Provider)
		}
		for _, prefix := range route.Prefix {
			if !validPrefix(prefix) {
				return fmt.Errorf("route %q has invalid prefix %q", route.Name, prefix)
			}
		}
	}
	esmeNames := map[string]struct{}{}
	for _, esme := range c.ESMEs {
		if esme.SystemID == "" {
			return fmt.Errorf("esme system_id is required")
		}
		if esme.Password == "" {
			return fmt.Errorf("esme %q password is required", esme.SystemID)
		}
		if isPlaceholder(esme.Password) {
			return fmt.Errorf("esme %q password must be changed before deploy", esme.SystemID)
		}
		if _, ok := esmeNames[esme.SystemID]; ok {
			return fmt.Errorf("duplicate esme %q", esme.SystemID)
		}
		esmeNames[esme.SystemID] = struct{}{}
	}
	clientNames := map[string]struct{}{}
	for _, client := range c.Clients {
		if client.ClientID == "" {
			return fmt.Errorf("client client_id is required")
		}
		if client.Enabled && client.Token == "" {
			return fmt.Errorf("client %q token is required", client.ClientID)
		}
		if isPlaceholder(client.Token) {
			return fmt.Errorf("client %q token must be changed before deploy", client.ClientID)
		}
		if _, ok := clientNames[client.ClientID]; ok {
			return fmt.Errorf("duplicate client %q", client.ClientID)
		}
		clientNames[client.ClientID] = struct{}{}
	}
	for _, rule := range c.Inbound {
		if rule.Name == "" {
			return fmt.Errorf("inbound rule name is required")
		}
		if rule.Path == "" || !strings.HasPrefix(rule.Path, "/") {
			return fmt.Errorf("inbound rule %q path must start with /", rule.Name)
		}
		if rule.AuthHeader == "" || rule.AuthToken == "" {
			return fmt.Errorf("inbound rule %q auth_header and auth_token are required", rule.Name)
		}
		if rule.Fields["from"] == "" || rule.Fields["to"] == "" || rule.Fields["text"] == "" {
			if rule.Fields["provider_id"] == "" || rule.Fields["status"] == "" {
				return fmt.Errorf("inbound rule %q must map from, to, and text", rule.Name)
			}
		}
		if rule.Fields["provider_id"] != "" || rule.Fields["status"] != "" {
			if rule.Fields["provider_id"] == "" || rule.Fields["status"] == "" {
				return fmt.Errorf("inbound rule %q must map provider_id and status together", rule.Name)
			}
			if rule.Provider == "" {
				return fmt.Errorf("inbound dlr rule %q provider is required", rule.Name)
			}
			if _, ok := names["provider:"+rule.Provider]; !ok {
				return fmt.Errorf("inbound dlr rule %q references unknown provider %q", rule.Name, rule.Provider)
			}
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

func isPlaceholder(value string) bool {
	return value == PlaceholderSecret
}

var prefixPattern = regexp.MustCompile(`^[0-9+*#]*$`)

func validPrefix(prefix string) bool {
	return prefixPattern.MatchString(prefix)
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
		cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			return cfg, fmt.Errorf("validate config: %w", err)
		}
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
