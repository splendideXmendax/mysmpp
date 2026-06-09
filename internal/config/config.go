package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const PlaceholderSecret = "CHANGE_ME_BEFORE_DEPLOY"
const AutoGenerateSecret = "AUTO_GENERATE_ON_FIRST_RUN"

const (
	DefaultHTTPAddr = "127.0.0.1:19087"
	DefaultSMPPAddr = "127.0.0.1:29175"
)

const (
	SMPPMaxSystemID   = 15
	SMPPMaxPassword   = 8
	SMPPMaxSystemType = 12
	SMPPMaxAddress    = 20
)

type Config struct {
	Server         ServerConfig     `json:"server"`
	SMPP           SMPPConfig       `json:"smpp"`
	Dispatcher     DispatcherConfig `json:"dispatcher"`
	ESMEs          []ESMECred       `json:"esmes"`
	Routes         []RouteConfig    `json:"routes"`
	Providers      []ProviderConfig `json:"providers"`
	Inbound        []HTTPRuleConfig `json:"inbound"`
	Outbound       []HTTPRuleConfig `json:"outbound"`
	Clients        []ClientAuth     `json:"clients"`
	TrustedProxies []string         `json:"trusted_proxies"`
	Risk           RiskConfig       `json:"risk"`
	Storage        StorageConfig    `json:"storage"`
	Admin          AdminConfig      `json:"admin"`
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

type DispatcherConfig struct {
	Workers              int    `json:"workers"`
	PerWorkerConcurrency int    `json:"per_worker_concurrency"`
	ClaimLimit           int    `json:"claim_limit"`
	PollIntervalMS       int    `json:"poll_interval_ms"`
	PendingTTL           string `json:"pending_ttl"`
	MaxAttempts          int    `json:"max_attempts"`
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
	Name          string            `json:"name"`
	Protocol      string            `json:"protocol"`
	Endpoint      string            `json:"endpoint"`
	Rule          string            `json:"rule"`
	SystemID      string            `json:"system_id"`
	Password      string            `json:"password"`
	Enabled       bool              `json:"enabled"`
	HTTPTimeoutMS int               `json:"http_timeout_ms"`
	RateLimit     ProviderRateLimit `json:"rate_limit"`
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
		Server: ServerConfig{HTTPAddr: DefaultHTTPAddr, ShutdownTimeout: "10s"},
		SMPP: SMPPConfig{
			Addr:                   DefaultSMPPAddr,
			SystemID:               "mysmpp",
			SystemType:             "gateway",
			MaxSessions:            128,
			MaxSessionsPerSystemID: 4,
			WindowSize:             16,
			EnquirePeriod:          "30s",
		},
		Dispatcher: DispatcherConfig{
			Workers:              10,
			PerWorkerConcurrency: 10,
			ClaimLimit:           20,
			PollIntervalMS:       20,
			PendingTTL:           "30m",
			MaxAttempts:          5,
		},
		Storage: StorageConfig{Driver: "memory"},
	}
}

func (c *Config) Normalize() {
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = DefaultHTTPAddr
	}
	if c.Server.ShutdownTimeout == "" {
		c.Server.ShutdownTimeout = "10s"
	}
	if c.SMPP.Addr == "" {
		c.SMPP.Addr = DefaultSMPPAddr
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
	if c.Dispatcher.Workers == 0 {
		c.Dispatcher.Workers = 10
	}
	if c.Dispatcher.PerWorkerConcurrency == 0 {
		c.Dispatcher.PerWorkerConcurrency = 10
	}
	if c.Dispatcher.ClaimLimit == 0 {
		c.Dispatcher.ClaimLimit = 20
	}
	if c.Dispatcher.PollIntervalMS == 0 {
		c.Dispatcher.PollIntervalMS = 20
	}
	if c.Dispatcher.PendingTTL == "" {
		c.Dispatcher.PendingTTL = "30m"
	}
	if c.Dispatcher.MaxAttempts == 0 {
		c.Dispatcher.MaxAttempts = 5
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
	return c.validate(false)
}

func (c Config) validate(allowAutoGenerate bool) error {
	switch strings.ToLower(c.Storage.Driver) {
	case "", "memory", "file", "json":
	case "postgres", "pg":
		if c.Storage.DSN == "" {
			return fmt.Errorf("storage.dsn is required for postgres")
		}
	default:
		return fmt.Errorf("unsupported storage.driver %q", c.Storage.Driver)
	}
	if c.Admin.Username == "" || c.Admin.Password == "" {
		return fmt.Errorf("admin username and password are required")
	}
	if isPlaceholder(c.Admin.Username) || isPlaceholder(c.Admin.Password) {
		return fmt.Errorf("admin credentials must be changed before deploy")
	}
	if !allowAutoGenerate && (IsAutoGenerate(c.Admin.Password) || IsAutoGenerate(c.SMPP.Password)) {
		return fmt.Errorf("auto-generate placeholders are only allowed during startup bootstrap")
	}
	if isPlaceholder(c.SMPP.Password) {
		return fmt.Errorf("smpp password must be changed before deploy")
	}
	if err := validateSMPPString("smpp.system_id", c.SMPP.SystemID, SMPPMaxSystemID, allowAutoGenerate); err != nil {
		return err
	}
	if err := validateSMPPString("smpp.password", c.SMPP.Password, SMPPMaxPassword, allowAutoGenerate); err != nil {
		return err
	}
	if err := validateSMPPString("smpp.system_type", c.SMPP.SystemType, SMPPMaxSystemType, allowAutoGenerate); err != nil {
		return err
	}
	if c.Server.HTTPAddr != "" && c.SMPP.Addr != "" && c.Server.HTTPAddr == c.SMPP.Addr {
		return fmt.Errorf("server.http_addr and smpp.addr must not be the same")
	}
	if c.Dispatcher.Workers < 0 || c.Dispatcher.PerWorkerConcurrency < 0 || c.Dispatcher.ClaimLimit < 0 ||
		c.Dispatcher.PollIntervalMS < 0 || c.Dispatcher.MaxAttempts < 0 {
		return fmt.Errorf("dispatcher values must be non-negative")
	}
	if _, err := time.ParseDuration(c.Dispatcher.PendingTTL); c.Dispatcher.PendingTTL != "" && err != nil {
		return fmt.Errorf("dispatcher.pending_ttl is invalid: %w", err)
	}
	for _, proxy := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(proxy); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(proxy); err != nil {
			return fmt.Errorf("trusted_proxies contains invalid ip or cidr %q", proxy)
		}
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
		if provider.HTTPTimeoutMS < 0 {
			return fmt.Errorf("provider %q http_timeout_ms must be non-negative", provider.Name)
		}
		if provider.RateLimit.TPS < 0 || provider.RateLimit.Burst < 0 || provider.RateLimit.TimeoutMS < 0 {
			return fmt.Errorf("provider %q rate_limit values must be non-negative", provider.Name)
		}
		if !allowAutoGenerate && IsAutoGenerate(provider.Password) {
			return fmt.Errorf("auto-generate placeholders are only allowed during startup bootstrap")
		}
	}
	if len(c.Providers) > 0 && len(enabledProviders) == 0 {
		return fmt.Errorf("at least one provider must be enabled")
	}
	for _, route := range c.Routes {
		if route.Name == "" {
			return fmt.Errorf("route name is required")
		}
		if !validName(route.Name) {
			return fmt.Errorf("route %q has invalid name", route.Name)
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
	if err := validateRoutePrefixes(c.Routes); err != nil {
		return err
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
		if !allowAutoGenerate && IsAutoGenerate(esme.Password) {
			return fmt.Errorf("auto-generate placeholders are only allowed during startup bootstrap")
		}
		if err := validateSMPPString("esme.system_id", esme.SystemID, SMPPMaxSystemID, allowAutoGenerate); err != nil {
			return err
		}
		if err := validateSMPPString("esme.password", esme.Password, SMPPMaxPassword, allowAutoGenerate); err != nil {
			return fmt.Errorf("esme %q %w", esme.SystemID, err)
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
		if !allowAutoGenerate && IsAutoGenerate(client.Token) {
			return fmt.Errorf("auto-generate placeholders are only allowed during startup bootstrap")
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
		if !validName(rule.Name) {
			return fmt.Errorf("inbound rule %q has invalid name", rule.Name)
		}
		if rule.Path == "" || !strings.HasPrefix(rule.Path, "/") {
			return fmt.Errorf("inbound rule %q path must start with /", rule.Name)
		}
		if isReservedHTTPPath(rule.Path) {
			return fmt.Errorf("inbound rule %q path %q conflicts with built-in route", rule.Name, rule.Path)
		}
		if rule.AuthHeader == "" || rule.AuthToken == "" {
			return fmt.Errorf("inbound rule %q auth_header and auth_token are required", rule.Name)
		}
		if !allowAutoGenerate && IsAutoGenerate(rule.AuthToken) {
			return fmt.Errorf("auto-generate placeholders are only allowed during startup bootstrap")
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

func validateSMPPString(name, value string, max int, allowAutoGenerate bool) error {
	if value == "" || (allowAutoGenerate && IsAutoGenerate(value)) {
		return nil
	}
	if len(value) > max {
		return fmt.Errorf("%s must be at most %d bytes", name, max)
	}
	return nil
}

func isPlaceholder(value string) bool {
	return value == PlaceholderSecret
}

func IsAutoGenerate(value string) bool {
	return value == AutoGenerateSecret
}

var prefixPattern = regexp.MustCompile(`^[0-9+*#]*$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func validPrefix(prefix string) bool {
	return prefixPattern.MatchString(prefix)
}

func validName(name string) bool {
	return namePattern.MatchString(name)
}

func validateRoutePrefixes(routes []RouteConfig) error {
	type owner struct {
		route  string
		prefix string
	}
	seen := []owner{}
	for _, route := range routes {
		for _, prefix := range route.Prefix {
			for _, prior := range seen {
				if prefix == prior.prefix || prefixDominates(prefix, prior.prefix) || prefixDominates(prior.prefix, prefix) {
					return fmt.Errorf("route %q prefix %q conflicts with route %q prefix %q", route.Name, prefix, prior.route, prior.prefix)
				}
			}
			seen = append(seen, owner{route: route.Name, prefix: prefix})
		}
	}
	return nil
}

func prefixDominates(a, b string) bool {
	return a != "" && b != "" && strings.HasPrefix(b, a)
}

func isReservedHTTPPath(path string) bool {
	switch {
	case path == "/healthz", path == "/v1/messages", path == "/v1/config", path == "/ui/config":
		return true
	case path == "/admin" || strings.HasPrefix(path, "/admin/"):
		return true
	default:
		return false
	}
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
	return load(path, false)
}

func load(path string, allowAutoGenerate bool) (Config, error) {
	cfg := Default()
	if path == "" {
		cfg.Normalize()
		if err := cfg.validate(allowAutoGenerate); err != nil {
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
	resolveStoragePath(path, &cfg)
	if err := cfg.validate(allowAutoGenerate); err != nil {
		return cfg, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func resolveStoragePath(configPath string, cfg *Config) {
	if !strings.EqualFold(cfg.Storage.Driver, "file") && !strings.EqualFold(cfg.Storage.Driver, "json") {
		return
	}
	if cfg.Storage.DSN == "" {
		cfg.Storage.DSN = filepath.Join(filepath.Dir(configPath), "store.json")
		return
	}
	if !filepath.IsAbs(cfg.Storage.DSN) {
		cfg.Storage.DSN = filepath.Join(filepath.Dir(configPath), cfg.Storage.DSN)
	}
}
