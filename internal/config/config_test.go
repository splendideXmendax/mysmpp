package config

import "testing"

func TestValidateInboundRequiresFieldMapping(t *testing.T) {
	cfg := Default()
	cfg.Inbound = []HTTPRuleConfig{{
		Name:   "callback",
		Path:   "/callback",
		Fields: map[string]string{"from": "src", "to": "dst"},
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
		t.Fatal(err)
	}
	if cfg.Server.HTTPAddr != ":8080" {
		t.Fatalf("unexpected http addr %s", cfg.Server.HTTPAddr)
	}
}

func TestLoadDockerConfig(t *testing.T) {
	cfg, err := Load("../../configs/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMPP.Password == "secret" {
		t.Fatal("docker config should not keep the development SMPP password")
	}
}
