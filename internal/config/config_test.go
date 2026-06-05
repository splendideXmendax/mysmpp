package config

import (
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

func TestLoadDockerConfig(t *testing.T) {
	cfg, err := Load("../../configs/docker.json")
	if err != nil {
		t.Fatalf("docker config should load: %v", err)
	}
	if cfg.Server.HTTPAddr != "0.0.0.0:19087" || cfg.SMPP.Addr != "0.0.0.0:29175" {
		t.Fatalf("unexpected docker ports: http=%q smpp=%q", cfg.Server.HTTPAddr, cfg.SMPP.Addr)
	}
	if cfg.Storage.Driver != "file" {
		t.Fatalf("expected docker config to use file store, got %q", cfg.Storage.Driver)
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
