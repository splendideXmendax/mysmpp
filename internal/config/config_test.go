package config

import "testing"

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

func TestLoadExampleConfigRejectsDeployPlaceholders(t *testing.T) {
	if _, err := Load("../../configs/example.json"); err == nil {
		t.Fatal("expected example config to reject deploy placeholders")
	}
}

func TestLoadDockerConfigRejectsDeployPlaceholders(t *testing.T) {
	if _, err := Load("../../configs/docker.json"); err == nil {
		t.Fatal("expected docker config to reject deploy placeholders")
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
