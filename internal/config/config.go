package config

import (
	"encoding/json"
	"fmt"
	"net"
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
	Filter         FilterConfig     `json:"filter"`
	CDR            CDRConfig        `json:"cdr"`
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
	ClaimTimeout         string `json:"claim_timeout"`
	PendingSweepInterval string `json:"pending_sweep_interval"`
	ValidateDestAddr     *bool  `json:"validate_dest_addr,omitempty"`
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
	Name        string             `json:"name"`
	Prefix      []string           `json:"prefix"`
	Provider    string             `json:"provider"`
	Priority    int                `json:"priority"`
	AddrRewrite AddrRewriteConfig  `json:"addr_rewrite,omitempty"`
	DestAddr    DestAddrConfig     `json:"dest_addr,omitempty"`
	Enabled     *bool              `json:"enabled,omitempty"`
	FromPrefix  []string           `json:"from_prefix,omitempty"`
	SystemIDs   []string           `json:"system_ids,omitempty"`
	ClientIDs   []string           `json:"client_ids,omitempty"`
	ContentTags []string           `json:"content_tags,omitempty"`
	TimeWindows []TimeWindow       `json:"time_windows,omitempty"`
	Weighted    []WeightedProvider `json:"weighted,omitempty"`
	Failover    []string           `json:"failover,omitempty"`
}

type WeightedProvider struct {
	Provider string `json:"provider"`
	Weight   int    `json:"weight"`
}

type TimeWindow struct {
	Days  []string `json:"days,omitempty"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

type FilterConfig struct {
	Enabled   bool            `json:"enabled"`
	Normalize NormalizeConfig `json:"normalize"`
	Rules     []FilterRule    `json:"rules"`
}

type NormalizeConfig struct {
	Lowercase      bool `json:"lowercase"`
	FullToHalf     bool `json:"full_to_half"`
	StripZeroWidth bool `json:"strip_zero_width"`
}

type FilterRule struct {
	Name     string   `json:"name"`
	Enabled  *bool    `json:"enabled,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	Regex    string   `json:"regex,omitempty"`
	Action   string   `json:"action"`
	Tag      string   `json:"tag,omitempty"`
	MaskWith string   `json:"mask_with,omitempty"`
	Priority int      `json:"priority"`
}

type CDRConfig struct {
	Enabled       bool   `json:"enabled"`
	Dir           string `json:"dir"`
	Mode          string `json:"mode"`
	MaxRecords    int    `json:"max_records"`
	MaxAge        string `json:"max_age"`
	Buffer        int    `json:"buffer"`
	OnFull        string `json:"on_full"`
	FsyncEvery    int    `json:"fsync_every"`
	FsyncInterval string `json:"fsync_interval"`
	Instance      string `json:"instance"`
	MaskTo        bool   `json:"mask_to"`
	StoreText     bool   `json:"store_text"`
}

type AddrRewriteConfig struct {
	StripTrunkZeroAfterCC bool   `json:"strip_trunk_zero_after_cc"`
	CountryCode           string `json:"country_code,omitempty"`
	AddPrefix             string `json:"add_prefix,omitempty"`
	EnforceE164Len        bool   `json:"enforce_e164_len,omitempty"`
}

type DestAddrConfig struct {
	Validate          *bool  `json:"validate,omitempty"`
	AllowShortCode    bool   `json:"allow_short_code,omitempty"`
	MinShortLen       int    `json:"min_short_len,omitempty"`
	MaxShortLen       int    `json:"max_short_len,omitempty"`
	CountryLengthMode string `json:"country_length_mode,omitempty"`
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
	SMPP          *SMPPClientConfig `json:"smpp,omitempty"`
}

type ProviderRateLimit struct {
	TPS       int `json:"tps"`
	Burst     int `json:"burst"`
	TimeoutMS int `json:"timeout_ms"`
}

type SMPPClientConfig struct {
	BindMode            string `json:"bind_mode"`
	SystemType          string `json:"system_type"`
	Binds               int    `json:"binds"`
	WindowSize          int    `json:"window_size"`
	EnquirePeriod       string `json:"enquire_period"`
	ResponseTimeoutMS   int    `json:"response_timeout_ms"`
	ReconnectMin        string `json:"reconnect_min"`
	ReconnectMax        string `json:"reconnect_max"`
	SourceTON           int    `json:"source_ton"`
	SourceNPI           int    `json:"source_npi"`
	DestTON             int    `json:"dest_ton"`
	DestNPI             int    `json:"dest_npi"`
	ServiceType         string `json:"service_type"`
	ValidityPeriod      string `json:"validity_period"`
	RegisteredDelivery  int    `json:"registered_delivery"`
	GSM7Packing         string `json:"gsm7_packing"`
	LongMessage         string `json:"long_message"`
	MessageIDRespFormat string `json:"message_id_resp_format"`
	MessageIDDLRFormat  string `json:"message_id_dlr_format"`
	DLRIDSource         string `json:"dlr_id_source"`
	RetryOnTimeout      bool   `json:"retry_on_timeout"`
	TLS                 bool   `json:"tls"`
}

func DefaultSMPPClientConfig() SMPPClientConfig {
	return SMPPClientConfig{
		BindMode:            "transceiver",
		Binds:               1,
		WindowSize:          16,
		EnquirePeriod:       "30s",
		ResponseTimeoutMS:   5000,
		ReconnectMin:        "1s",
		ReconnectMax:        "60s",
		SourceTON:           -1,
		SourceNPI:           -1,
		DestTON:             1,
		DestNPI:             1,
		RegisteredDelivery:  -1,
		GSM7Packing:         "unpacked",
		LongMessage:         "udh",
		MessageIDRespFormat: "auto",
		MessageIDDLRFormat:  "auto",
		DLRIDSource:         "auto",
	}
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
			ClaimTimeout:         "60s",
			PendingSweepInterval: "1m",
			ValidateDestAddr:     boolPtr(true),
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
	if c.SMPP.EnquirePeriod == "" {
		c.SMPP.EnquirePeriod = "30s"
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
	if c.Dispatcher.ClaimTimeout == "" {
		c.Dispatcher.ClaimTimeout = "60s"
	}
	if c.Dispatcher.PendingSweepInterval == "" {
		c.Dispatcher.PendingSweepInterval = "1m"
	}
	if c.Dispatcher.ValidateDestAddr == nil {
		c.Dispatcher.ValidateDestAddr = boolPtr(true)
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
	if c.CDR.Enabled {
		if c.CDR.Dir == "" {
			c.CDR.Dir = "data/cdr"
		}
		if c.CDR.Mode == "" {
			c.CDR.Mode = "events"
		}
		if c.CDR.MaxRecords == 0 {
			c.CDR.MaxRecords = 10000
		}
		if c.CDR.MaxAge == "" {
			c.CDR.MaxAge = "1h"
		}
		if c.CDR.Buffer == 0 {
			c.CDR.Buffer = 65536
		}
		if c.CDR.OnFull == "" {
			c.CDR.OnFull = "block"
		}
		if c.CDR.FsyncInterval == "" {
			c.CDR.FsyncInterval = "2s"
		}
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
	for i := range c.Providers {
		if c.Providers[i].SMPP != nil {
			c.Providers[i].SMPP.Normalize()
		}
	}
	for i := range c.Routes {
		if c.Routes[i].Enabled == nil {
			c.Routes[i].Enabled = boolPtr(true)
		}
		for j := range c.Routes[i].Weighted {
			if c.Routes[i].Weighted[j].Weight <= 0 {
				c.Routes[i].Weighted[j].Weight = 1
			}
		}
	}
}

func (c *SMPPClientConfig) Normalize() {
	if c.BindMode == "" {
		c.BindMode = "transceiver"
	}
	if c.Binds == 0 {
		c.Binds = 1
	}
	if c.WindowSize == 0 {
		c.WindowSize = 16
	}
	if c.EnquirePeriod == "" {
		c.EnquirePeriod = "30s"
	}
	if c.ResponseTimeoutMS == 0 {
		c.ResponseTimeoutMS = 5000
	}
	if c.ReconnectMin == "" {
		c.ReconnectMin = "1s"
	}
	if c.ReconnectMax == "" {
		c.ReconnectMax = "60s"
	}
	if c.GSM7Packing == "" {
		c.GSM7Packing = "unpacked"
	}
	if c.LongMessage == "" {
		c.LongMessage = "udh"
	}
	if c.MessageIDRespFormat == "" {
		c.MessageIDRespFormat = "auto"
	}
	if c.MessageIDDLRFormat == "" {
		c.MessageIDDLRFormat = "auto"
	}
	if c.DLRIDSource == "" {
		c.DLRIDSource = "auto"
	}
}

func (c *SMPPClientConfig) UnmarshalJSON(data []byte) error {
	type alias SMPPClientConfig
	defaults := alias(DefaultSMPPClientConfig())
	if err := json.Unmarshal(data, &defaults); err != nil {
		return err
	}
	*c = SMPPClientConfig(defaults)
	return nil
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
	if _, err := time.ParseDuration(c.Dispatcher.ClaimTimeout); c.Dispatcher.ClaimTimeout != "" && err != nil {
		return fmt.Errorf("dispatcher.claim_timeout is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Dispatcher.PendingSweepInterval); c.Dispatcher.PendingSweepInterval != "" && err != nil {
		return fmt.Errorf("dispatcher.pending_sweep_interval is invalid: %w", err)
	}
	if err := validateClaimTimeoutInvariant(c); err != nil {
		return err
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
		if err := validateProviderProtocol(provider, allowAutoGenerate); err != nil {
			return err
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
		if route.Provider == "" && len(route.Weighted) == 0 && len(route.Failover) == 0 {
			return fmt.Errorf("route %q provider is required", route.Name)
		}
		if len(route.Weighted) > 0 && len(route.Failover) > 0 {
			return fmt.Errorf("route %q cannot define both weighted and failover", route.Name)
		}
		for _, wp := range route.Weighted {
			if wp.Provider == "" {
				return fmt.Errorf("route %q weighted provider is required", route.Name)
			}
			if wp.Weight <= 0 {
				return fmt.Errorf("route %q weighted provider %q weight must be > 0", route.Name, wp.Provider)
			}
			if _, ok := names["provider:"+wp.Provider]; !ok {
				return fmt.Errorf("route %q references unknown weighted provider %q", route.Name, wp.Provider)
			}
		}
		for _, name := range route.Failover {
			if _, ok := names["provider:"+name]; !ok {
				return fmt.Errorf("route %q references unknown failover provider %q", route.Name, name)
			}
		}
		if route.Provider != "" {
			if _, ok := names["provider:"+route.Provider]; !ok {
				return fmt.Errorf("route %q references unknown provider %q", route.Name, route.Provider)
			}
		}
		for _, prefix := range route.Prefix {
			if !validPrefix(prefix) {
				return fmt.Errorf("route %q has invalid prefix %q", route.Name, prefix)
			}
		}
		for _, prefix := range route.FromPrefix {
			if !validPrefix(prefix) {
				return fmt.Errorf("route %q has invalid from_prefix %q", route.Name, prefix)
			}
		}
		for _, window := range route.TimeWindows {
			if err := validateTimeWindow(route.Name, window); err != nil {
				return err
			}
		}
		if route.AddrRewrite.CountryCode != "" && !allDigits(route.AddrRewrite.CountryCode) {
			return fmt.Errorf("route %q addr_rewrite.country_code must contain only digits", route.Name)
		}
		if route.AddrRewrite.AddPrefix != "" && !allDigits(route.AddrRewrite.AddPrefix) {
			return fmt.Errorf("route %q addr_rewrite.add_prefix must contain only digits", route.Name)
		}
		if route.DestAddr.MinShortLen < 0 || route.DestAddr.MaxShortLen < 0 {
			return fmt.Errorf("route %q dest_addr short code lengths must be non-negative", route.Name)
		}
		if route.DestAddr.MinShortLen > 0 && route.DestAddr.MaxShortLen > 0 && route.DestAddr.MinShortLen > route.DestAddr.MaxShortLen {
			return fmt.Errorf("route %q dest_addr min_short_len must be <= max_short_len", route.Name)
		}
		switch route.DestAddr.CountryLengthMode {
		case "", "off", "compat", "strict":
		default:
			return fmt.Errorf("route %q dest_addr.country_length_mode must be off, compat, or strict", route.Name)
		}
	}
	if err := validateRoutePrefixes(c.Routes); err != nil {
		return err
	}
	if err := validateFilter(c.Filter); err != nil {
		return err
	}
	if err := validateCDR(c.CDR); err != nil {
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

func validateProviderProtocol(provider ProviderConfig, allowAutoGenerate bool) error {
	protocol := strings.ToLower(provider.Protocol)
	switch protocol {
	case "", "mock":
		if provider.SMPP != nil {
			return fmt.Errorf("provider %q smpp config is only valid when protocol is smpp", provider.Name)
		}
	case "http", "https":
		if provider.SMPP != nil {
			return fmt.Errorf("provider %q smpp config is only valid when protocol is smpp", provider.Name)
		}
		if provider.Rule == "" {
			return fmt.Errorf("provider %q rule is required for http provider", provider.Name)
		}
	case "smpp":
		if provider.SMPP == nil {
			return fmt.Errorf("provider %q smpp config is required", provider.Name)
		}
		if provider.Endpoint == "" {
			return fmt.Errorf("provider %q endpoint is required for smpp provider", provider.Name)
		}
		host, port, err := net.SplitHostPort(provider.Endpoint)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("provider %q endpoint must be host:port", provider.Name)
		}
		if provider.Rule != "" {
			return fmt.Errorf("provider %q rule must be empty for smpp provider", provider.Name)
		}
		if provider.SystemID == "" {
			return fmt.Errorf("provider %q system_id is required for smpp provider", provider.Name)
		}
		if provider.Password == "" {
			return fmt.Errorf("provider %q password is required for smpp provider", provider.Name)
		}
		if isPlaceholder(provider.Password) {
			return fmt.Errorf("provider %q password must be changed before deploy", provider.Name)
		}
		if err := validateSMPPString("provider.system_id", provider.SystemID, SMPPMaxSystemID, allowAutoGenerate); err != nil {
			return fmt.Errorf("provider %q %w", provider.Name, err)
		}
		if err := validateSMPPString("provider.password", provider.Password, SMPPMaxPassword, allowAutoGenerate); err != nil {
			return fmt.Errorf("provider %q %w", provider.Name, err)
		}
		if err := validateSMPPString("provider.smpp.system_type", provider.SMPP.SystemType, SMPPMaxSystemType, allowAutoGenerate); err != nil {
			return fmt.Errorf("provider %q %w", provider.Name, err)
		}
		if err := validateSMPPClientConfig(provider.Name, *provider.SMPP); err != nil {
			return err
		}
	default:
		return fmt.Errorf("provider %q unsupported protocol %q", provider.Name, provider.Protocol)
	}
	return nil
}

func validateSMPPClientConfig(providerName string, cfg SMPPClientConfig) error {
	if cfg.BindMode != "transceiver" && cfg.BindMode != "tx_rx" {
		return fmt.Errorf("provider %q smpp.bind_mode must be transceiver or tx_rx", providerName)
	}
	if cfg.BindMode == "tx_rx" {
		return fmt.Errorf("provider %q smpp.bind_mode tx_rx is not implemented yet", providerName)
	}
	if cfg.Binds < 1 {
		return fmt.Errorf("provider %q smpp.binds must be >= 1", providerName)
	}
	if cfg.WindowSize < 1 {
		return fmt.Errorf("provider %q smpp.window_size must be >= 1", providerName)
	}
	if cfg.ResponseTimeoutMS < 1 {
		return fmt.Errorf("provider %q smpp.response_timeout_ms must be >= 1", providerName)
	}
	for field, value := range map[string]string{
		"enquire_period": cfg.EnquirePeriod,
		"reconnect_min":  cfg.ReconnectMin,
		"reconnect_max":  cfg.ReconnectMax,
	} {
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("provider %q smpp.%s is invalid: %w", providerName, field, err)
		}
	}
	if !validTONNPI(cfg.SourceTON, true) {
		return fmt.Errorf("provider %q smpp.source_ton must be -1 or 0..6", providerName)
	}
	if !validTONNPI(cfg.SourceNPI, true) {
		return fmt.Errorf("provider %q smpp.source_npi must be -1 or 0..6", providerName)
	}
	if !validTONNPI(cfg.DestTON, false) {
		return fmt.Errorf("provider %q smpp.dest_ton must be 0..6", providerName)
	}
	if !validTONNPI(cfg.DestNPI, false) {
		return fmt.Errorf("provider %q smpp.dest_npi must be 0..6", providerName)
	}
	if cfg.RegisteredDelivery < -1 || cfg.RegisteredDelivery > 1 {
		return fmt.Errorf("provider %q smpp.registered_delivery must be -1, 0, or 1", providerName)
	}
	if cfg.GSM7Packing != "unpacked" && cfg.GSM7Packing != "packed" {
		return fmt.Errorf("provider %q smpp.gsm7_packing must be unpacked or packed", providerName)
	}
	if cfg.LongMessage != "udh" && cfg.LongMessage != "payload" && cfg.LongMessage != "sar" {
		return fmt.Errorf("provider %q smpp.long_message must be udh, payload, or sar", providerName)
	}
	if !validIDFormat(cfg.MessageIDRespFormat) {
		return fmt.Errorf("provider %q smpp.message_id_resp_format must be auto, dec, or hex", providerName)
	}
	if !validIDFormat(cfg.MessageIDDLRFormat) {
		return fmt.Errorf("provider %q smpp.message_id_dlr_format must be auto, dec, or hex", providerName)
	}
	if cfg.DLRIDSource != "auto" && cfg.DLRIDSource != "text" && cfg.DLRIDSource != "tlv" {
		return fmt.Errorf("provider %q smpp.dlr_id_source must be auto, text, or tlv", providerName)
	}
	if cfg.TLS {
		return fmt.Errorf("provider %q smpp.tls is not implemented yet", providerName)
	}
	return nil
}

func validTONNPI(value int, allowAuto bool) bool {
	if allowAuto && value == -1 {
		return true
	}
	return value >= 0 && value <= 6
}

func validIDFormat(value string) bool {
	return value == "auto" || value == "dec" || value == "hex"
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

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func boolPtr(value bool) *bool {
	return &value
}

func (c DispatcherConfig) ValidateDestAddrEnabled() bool {
	return c.ValidateDestAddr == nil || *c.ValidateDestAddr
}

func (c DestAddrConfig) ValidateEnabled(global bool) bool {
	if c.Validate == nil {
		return global
	}
	return *c.Validate
}

func validateClaimTimeoutInvariant(c Config) error {
	claimTimeout, err := time.ParseDuration(c.Dispatcher.ClaimTimeout)
	if err != nil || claimTimeout <= 0 {
		return nil
	}
	perWorker := c.Dispatcher.PerWorkerConcurrency
	if perWorker <= 0 {
		perWorker = 10
	}
	claimLimit := c.Dispatcher.ClaimLimit
	if claimLimit <= 0 {
		claimLimit = 20
	}
	maxProviderTimeout := maxConfiguredProviderTimeout(c.Providers)
	if maxProviderTimeout <= 0 {
		maxProviderTimeout = 5 * time.Second
	}
	batches := (claimLimit + perWorker - 1) / perWorker
	required := time.Duration(batches) * maxProviderTimeout
	if claimTimeout <= required {
		return fmt.Errorf("dispatcher.claim_timeout (%s) must be greater than ceil(claim_limit/per_worker_concurrency) * max provider response timeout (%s); upstream delivery is at-least-once and stale-claim recovery may redeliver", claimTimeout, required)
	}
	return nil
}

func maxConfiguredProviderTimeout(providers []ProviderConfig) time.Duration {
	var maxTimeout time.Duration
	for _, p := range providers {
		var timeout time.Duration
		switch strings.ToLower(p.Protocol) {
		case "smpp":
			if p.SMPP != nil && p.SMPP.ResponseTimeoutMS > 0 {
				timeout = time.Duration(p.SMPP.ResponseTimeoutMS) * time.Millisecond
			} else {
				timeout = 5 * time.Second
			}
		case "http", "https":
			if p.HTTPTimeoutMS > 0 {
				timeout = time.Duration(p.HTTPTimeoutMS) * time.Millisecond
			} else {
				timeout = 3 * time.Second
			}
		default:
			timeout = 4 * time.Second
		}
		if timeout > maxTimeout {
			maxTimeout = timeout
		}
	}
	return maxTimeout
}

func validateRoutePrefixes(routes []RouteConfig) error {
	type owner struct {
		route    string
		prefix   string
		provider string
		priority int
	}
	seen := []owner{}
	for _, route := range routes {
		for _, prefix := range route.Prefix {
			for _, prior := range seen {
				if prefix == prior.prefix && route.Provider == prior.provider && route.Priority == prior.priority {
					return fmt.Errorf("route %q prefix %q conflicts with route %q prefix %q", route.Name, prefix, prior.route, prior.prefix)
				}
			}
			seen = append(seen, owner{route: route.Name, prefix: prefix, provider: route.Provider, priority: route.Priority})
		}
	}
	return nil
}

func validateFilter(cfg FilterConfig) error {
	if !cfg.Enabled {
		return nil
	}
	for _, rule := range cfg.Rules {
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		if rule.Name == "" {
			return fmt.Errorf("filter rule name is required")
		}
		if !validName(rule.Name) {
			return fmt.Errorf("filter rule %q has invalid name", rule.Name)
		}
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		switch action {
		case "block", "mask", "tag", "pass":
		default:
			return fmt.Errorf("filter rule %q has invalid action %q", rule.Name, rule.Action)
		}
		if len(rule.Keywords) == 0 && rule.Regex == "" {
			return fmt.Errorf("filter rule %q must define keywords or regex", rule.Name)
		}
		if action == "tag" && rule.Tag == "" {
			return fmt.Errorf("filter rule %q tag is required for tag action", rule.Name)
		}
		if rule.Regex != "" {
			if _, err := regexp.Compile(rule.Regex); err != nil {
				return fmt.Errorf("filter rule %q regex is invalid: %w", rule.Name, err)
			}
		}
	}
	return nil
}

func validateCDR(cfg CDRConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Dir == "" {
		return fmt.Errorf("cdr.dir is required")
	}
	if cfg.Mode != "" && cfg.Mode != "events" && cfg.Mode != "settled" {
		return fmt.Errorf("cdr.mode must be events or settled")
	}
	if cfg.MaxRecords <= 0 {
		return fmt.Errorf("cdr.max_records must be > 0")
	}
	if cfg.MaxAge != "" {
		if d, err := time.ParseDuration(cfg.MaxAge); err != nil || d <= 0 {
			return fmt.Errorf("cdr.max_age is invalid")
		}
	}
	if cfg.Buffer < 0 {
		return fmt.Errorf("cdr.buffer must be non-negative")
	}
	if cfg.OnFull != "" && cfg.OnFull != "block" && cfg.OnFull != "drop" {
		return fmt.Errorf("cdr.on_full must be block or drop")
	}
	if cfg.FsyncEvery < 0 {
		return fmt.Errorf("cdr.fsync_every must be non-negative")
	}
	if cfg.FsyncInterval != "" {
		if d, err := time.ParseDuration(cfg.FsyncInterval); err != nil || d < 0 {
			return fmt.Errorf("cdr.fsync_interval is invalid")
		}
	}
	return nil
}

func validateTimeWindow(route string, window TimeWindow) error {
	if _, err := parseClock(window.Start); err != nil {
		return fmt.Errorf("route %q time_window.start is invalid: %w", route, err)
	}
	if _, err := parseClock(window.End); err != nil {
		return fmt.Errorf("route %q time_window.end is invalid: %w", route, err)
	}
	for _, day := range window.Days {
		switch strings.ToLower(day) {
		case "mon", "monday", "tue", "tuesday", "wed", "wednesday", "thu", "thursday", "fri", "friday", "sat", "saturday", "sun", "sunday":
		default:
			return fmt.Errorf("route %q time_window day %q is invalid", route, day)
		}
	}
	return nil
}

func parseClock(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("expected HH:MM")
	}
	h, err := parseTwoDigit(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour")
	}
	m, err := parseTwoDigit(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute")
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

func parseTwoDigit(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a digit")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
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
