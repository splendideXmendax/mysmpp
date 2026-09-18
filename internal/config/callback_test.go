package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCallbackForwardingValidationAndRoundTrip(t *testing.T) {
	rule := CallbackForwarding{TenantID: "test-c", CallbackURL: "http://callbacks.example:2761/sms/dr", TargetURL: "http://10.20.30.40:2761/sms/dr"}
	cfg := validConfigForTest()
	cfg.Tenants = []TenantConfig{{TenantID: rule.TenantID}}
	cfg.Dispatcher.CallbackForwarding = []CallbackForwarding{rule}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	var restored Config
	if err := json.Unmarshal(raw, &restored); err != nil || !reflect.DeepEqual(restored.Dispatcher.CallbackForwarding, cfg.Dispatcher.CallbackForwarding) {
		t.Fatalf("lost forwarding config: %v", err)
	}
	for _, tc := range []struct{ name, source, target string }{
		{"https-downgrade", "https://callbacks.example/dr", rule.TargetURL},
		{"credentials", "http://user:secret@callbacks.example/dr", rule.TargetURL},
		{"query", rule.CallbackURL + "?target=other", rule.TargetURL},
		{"empty-query", rule.CallbackURL + "?", rule.TargetURL},
		{"fragment", rule.CallbackURL + "#", rule.TargetURL},
		{"no-path", "http://callbacks.example", rule.TargetURL},
		{"encoded-path", "http://callbacks.example/%64r", rule.TargetURL},
		{"port-range", "http://callbacks.example:65536/dr", rule.TargetURL},
		{"loopback", rule.CallbackURL, "http://127.0.0.1:2761/dr"},
		{"metadata", rule.CallbackURL, "http://169.254.169.254:80/latest/meta-data"},
		{"public", rule.CallbackURL, "http://8.8.8.8:2761/dr"},
		{"dynamic-dns", rule.CallbackURL, "http://internal.example:2761/dr"},
		{"missing-port", rule.CallbackURL, "http://10.20.30.40/dr"},
		{"target-query", rule.CallbackURL, rule.TargetURL + "?url=other"},
		{"mapped-loopback", rule.CallbackURL, "http://[::ffff:127.0.0.1]:2761/dr"},
		{"ipv6-zone", rule.CallbackURL, "http://[fd00::1%25eth0]:2761/dr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := rule
			bad.CallbackURL, bad.TargetURL = tc.source, tc.target
			cfg.Dispatcher.CallbackForwarding = []CallbackForwarding{bad}
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe forwarding config accepted")
			}
		})
	}
	cfg.Dispatcher.CallbackForwarding = []CallbackForwarding{rule, rule}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate mapping accepted")
	}
	cfg.Dispatcher.CallbackForwarding = []CallbackForwarding{rule}
	cfg.Tenants = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown tenant accepted")
	}
}
