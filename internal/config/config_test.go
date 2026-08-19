package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateInboundRequiresFieldMapping(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Inbound = []HTTPRuleConfig{{
		Name:       "callback",
		Path:       "/callback",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
		Fields:     map[string]string{"from": "src", "to": "dst"},
	}}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateTenantBindingsAndLimits(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Tenants = []TenantConfig{{
		TenantID: "customer-a",
		Limits:   TenantLimits{TPS: 20, Burst: 40, DailySegments: 1000, Timezone: "Asia/Shanghai"},
	}}
	cfg.Clients = []ClientAuth{{ClientID: "http-a", Token: "secret", Enabled: true, TenantID: "customer-a"}}
	cfg.ESMEs = []ESMECred{{SystemID: "smpp-a", Password: "secret", TenantID: "customer-a"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid tenant config rejected: %v", err)
	}

	cfg.Clients[0].TenantID = "missing"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown tenant reference was accepted")
	}
	cfg.Clients[0].TenantID = "customer-a"
	cfg.Tenants[0].Limits.Timezone = "Mars/Olympus"
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid tenant timezone was accepted")
	}
}

func TestESMEEnabledDefaultsToTrue(t *testing.T) {
	if !(ESMECred{}).EnabledValue() {
		t.Fatal("omitted enabled must preserve legacy enabled behavior")
	}
	disabled := false
	if (ESMECred{Enabled: &disabled}).EnabledValue() {
		t.Fatal("explicitly disabled ESME was enabled")
	}
}

func TestValidateInboundRequiresAuth(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Inbound = []HTTPRuleConfig{{
		Name:   "callback",
		Path:   "/callback",
		Fields: map[string]string{"from": "src", "to": "dst", "text": "msg"},
	}}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateInboundDLRRequiresProvider(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Inbound = []HTTPRuleConfig{{
		Name:       "dlr",
		Path:       "/callback/dlr",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
		Fields:     map[string]string{"provider_id": "id", "status": "state"},
	}}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNormalizeOutboundDefaults(t *testing.T) {
	cfg := Default()
	cfg.Outbound = []HTTPRuleConfig{{
		Name:   "provider",
		Fields: map[string]string{"to": "mobile", "text": "msg"},
	}}

	cfg.Normalize()
	if cfg.Outbound[0].Method != "POST" {
		t.Fatalf("expected POST, got %s", cfg.Outbound[0].Method)
	}
	if cfg.Outbound[0].ContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("unexpected content type %s", cfg.Outbound[0].ContentType)
	}
}

func TestNormalizeDispatcherDefaults(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if cfg.Dispatcher.Workers != 10 ||
		cfg.Dispatcher.PerWorkerConcurrency != 10 ||
		cfg.Dispatcher.ClaimLimit != 20 ||
		cfg.Dispatcher.PollIntervalMS != 20 ||
		cfg.Dispatcher.PendingTTL != "30m" ||
		cfg.Dispatcher.MaxAttempts != 5 ||
		cfg.Dispatcher.ClaimTimeout != "60s" ||
		!cfg.Dispatcher.ValidateDestAddrEnabled() {
		t.Fatalf("unexpected dispatcher defaults: %+v", cfg.Dispatcher)
	}
}

func TestDispatcherValidateDestAddrCanBeDisabled(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"dispatcher":{"validate_dest_addr":false}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Normalize()
	if cfg.Dispatcher.ValidateDestAddrEnabled() {
		t.Fatal("expected destination validation to be disabled")
	}
}

func TestValidateRejectsOverlongSMPPCredentials(t *testing.T) {
	cfg := validConfigForTest()
	cfg.ESMEs = []ESMECred{{SystemID: "client-a", Password: "too-long-password"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected overlong esme password to fail")
	}
}

func TestValidateRejectsInboundBuiltInPathConflict(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Inbound = []HTTPRuleConfig{{
		Name:       "messages",
		Path:       "/v1/messages",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
		Fields:     map[string]string{"from": "src", "to": "dst", "text": "msg"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected inbound rule path conflict to fail")
	}
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("../../configs/example.json")
	if err != nil {
		t.Fatalf("example config should load: %v", err)
	}
	if cfg.Server.HTTPAddr != DefaultHTTPAddr || cfg.SMPP.Addr != DefaultSMPPAddr {
		t.Fatalf("unexpected example ports: http=%q smpp=%q", cfg.Server.HTTPAddr, cfg.SMPP.Addr)
	}
}

func TestLoadProductionExampleConfigRejectsDeployPlaceholders(t *testing.T) {
	if _, err := Load("../../configs/production.example.json"); err == nil {
		t.Fatal("expected production example config to reject deploy placeholders")
	}
}

func TestDefaultConfigKeepsCredentialsOutOfCode(t *testing.T) {
	cfg := Default()
	cfg.Normalize()
	if cfg.Server.HTTPAddr != DefaultHTTPAddr || cfg.SMPP.Addr != DefaultSMPPAddr {
		t.Fatalf("unexpected default ports: http=%q smpp=%q", cfg.Server.HTTPAddr, cfg.SMPP.Addr)
	}
	if cfg.Admin.Username != "" || cfg.Admin.Password != "" || cfg.SMPP.Password != "" || len(cfg.ESMEs) != 0 {
		t.Fatalf("default config should not embed credentials: %+v", cfg)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("default config without external credentials should not validate")
	}
}

func TestLoadDevConfig(t *testing.T) {
	cfg, err := Load("../../configs/dev.json")
	if err != nil {
		t.Fatalf("dev config should load: %v", err)
	}
	if cfg.Server.HTTPAddr != DefaultHTTPAddr || cfg.SMPP.Addr != DefaultSMPPAddr {
		t.Fatalf("unexpected dev ports: http=%q smpp=%q", cfg.Server.HTTPAddr, cfg.SMPP.Addr)
	}
}

func TestLoadDockerConfigRejectsAutoGeneratePlaceholders(t *testing.T) {
	if _, err := Load("../../configs/docker.json"); err == nil {
		t.Fatal("docker seed config should require startup bootstrap")
	}
}

func TestValidateRejectsAutoGeneratePlaceholdersOutsideBootstrap(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Admin.Password = AutoGenerateSecret
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected auto-generate admin password to fail normal validation")
	}

	cfg = validConfigForTest()
	cfg.SMPP.Password = AutoGenerateSecret
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected auto-generate smpp password to fail normal validation")
	}
}

func TestValidatePostgresStorageRequiresDSN(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Storage.Driver = "postgres"
	cfg.Storage.DSN = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected postgres storage without dsn to fail")
	}

	cfg.Storage.DSN = "postgres://mysmpp:secret@localhost/mysmpp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected postgres storage with dsn to validate: %v", err)
	}
}

func TestValidateRejectsUnknownStorageDriver(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Storage.Driver = "sqlite"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown storage driver to fail")
	}
}

func TestValidateRejectsInvalidAddrRewriteConfig(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Routes[0].AddrRewrite.CountryCode = "86x"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid country code to fail")
	}

	cfg = validConfigForTest()
	cfg.Routes[0].AddrRewrite.AddPrefix = "+86"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid add prefix to fail")
	}

	cfg = validConfigForTest()
	cfg.Routes[0].DestAddr.CountryLengthMode = "unknown"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid country length mode to fail")
	}
}

func TestSMPPProviderConfigDefaultsAndValidation(t *testing.T) {
	payload := []byte(`{
		"server":{"http_addr":"127.0.0.1:19087","shutdown_timeout":"10s"},
		"smpp":{"addr":"127.0.0.1:29175","system_id":"mysmpp","password":"smpppw1","system_type":"gateway"},
		"dispatcher":{"pending_ttl":"48h"},
		"providers":[{
			"name":"smsc-a",
			"protocol":"smpp",
			"endpoint":"127.0.0.1:2775",
			"system_id":"acct",
			"password":"secret88",
			"enabled":true,
			"smpp":{}
		}],
		"routes":[{"name":"default","prefix":[],"provider":"smsc-a","priority":1}],
		"admin":{"username":"admin","password":"secret"}
	}`)
	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected smpp provider config to validate: %v", err)
	}
	smppCfg := cfg.Providers[0].SMPP
	if smppCfg.BindMode != "transceiver" ||
		smppCfg.SourceTON != -1 ||
		smppCfg.SourceNPI != -1 ||
		smppCfg.DestTON != 1 ||
		smppCfg.DestNPI != 1 ||
		smppCfg.RegisteredDelivery != -1 ||
		smppCfg.GSM7Packing != "unpacked" ||
		smppCfg.LongMessage != "udh" {
		t.Fatalf("unexpected smpp provider defaults: %+v", smppCfg)
	}
}

func TestValidateRejectsInvalidSMPPProvider(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Providers = []ProviderConfig{{
		Name:     "smsc-a",
		Protocol: "smpp",
		Endpoint: "127.0.0.1:2775",
		SystemID: "acct",
		Password: "too-long-password",
		Enabled:  true,
		SMPP:     &SMPPClientConfig{BindMode: "transceiver", Binds: 1, WindowSize: 1, EnquirePeriod: "30s", ResponseTimeoutMS: 5000, ReconnectMin: "1s", ReconnectMax: "60s", SourceTON: -1, SourceNPI: -1, DestTON: 1, DestNPI: 1, RegisteredDelivery: -1, GSM7Packing: "unpacked", LongMessage: "udh", MessageIDRespFormat: "auto", MessageIDDLRFormat: "auto", DLRIDSource: "auto"},
	}}
	cfg.Routes = []RouteConfig{{Name: "default", Provider: "smsc-a"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected overlong smpp provider password to fail")
	}

	cfg = validConfigForTest()
	cfg.Providers = []ProviderConfig{{
		Name:     "http-a",
		Protocol: "http",
		Endpoint: "https://example.com/send",
		Rule:     "rule-a",
		Enabled:  true,
		SMPP:     &SMPPClientConfig{},
	}}
	cfg.Outbound = []HTTPRuleConfig{{Name: "rule-a", Fields: map[string]string{"mobile": "to", "msg": "text"}}}
	cfg.Routes = []RouteConfig{{Name: "default", Provider: "http-a"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected http provider with smpp section to fail")
	}
}

func TestLoadStartupSeedsAndGeneratesSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg, boot, err := LoadStartup(configPath, "../../configs/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	if !boot.Seeded || !boot.Generated {
		t.Fatalf("expected seed and generated credentials, got %+v", boot)
	}
	if cfg.Admin.Password == AutoGenerateSecret || cfg.Admin.Password == "" {
		t.Fatalf("admin password was not generated: %q", cfg.Admin.Password)
	}
	if len(cfg.ESMEs) == 0 || cfg.ESMEs[0].Password == AutoGenerateSecret || cfg.ESMEs[0].Password == "" {
		t.Fatalf("esme password was not generated: %+v", cfg.ESMEs)
	}
	if cfg.Storage.DSN != filepath.Join(dir, "store.json") {
		t.Fatalf("expected storage dsn beside config, got %q", cfg.Storage.DSN)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("generated config missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.txt")); err != nil {
		t.Fatalf("generated credentials missing: %v", err)
	}

	cfg2, boot2, err := LoadStartup(configPath, "../../configs/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	if boot2.Generated {
		t.Fatalf("second startup should not regenerate credentials: %+v", boot2)
	}
	if cfg2.Admin.Password != cfg.Admin.Password || cfg2.ESMEs[0].Password != cfg.ESMEs[0].Password {
		t.Fatal("credentials changed on second startup")
	}
}

func validConfigForTest() Config {
	cfg := Default()
	cfg.Admin = AdminConfig{Username: "admin", Password: "secret"}
	cfg.SMPP.Password = "secret"
	cfg.Providers = []ProviderConfig{{
		Name:    "mock-a",
		Enabled: true,
	}}
	cfg.Routes = []RouteConfig{{
		Name:     "default",
		Provider: "mock-a",
	}}
	return cfg
}
