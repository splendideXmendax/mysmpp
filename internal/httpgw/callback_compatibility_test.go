package httpgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func TestCallbackAdmissionCompatibilityAndIdempotency(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		accepted  bool
	}{
		{"omitted", "", true},
		{"http", "http://callbacks.example:8080/dr?order=1", true},
		{"https", "https://callbacks.example/dr?order=1", true},
		{"relative", "/dr", false},
		{"unsupported", "ftp://callbacks.example/dr", false},
		{"credentials", "http://user:password@callbacks.example/dr", false},
		{"fragment", "https://callbacks.example/dr#fragment", false},
		{"missing-host", "http://:8080/dr", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			enableClientAuth(&cfg)
			cfg.Risk = config.RiskConfig{PerNumberPerMinute: -1, PerNumberPerDay: -1, PerClientPerSecond: -1}
			cfg.Clients[0].TenantID = "customer"
			cfg.Tenants = []config.TenantConfig{{TenantID: "customer", Limits: config.TenantLimits{DailySegments: 1, Timezone: "UTC"}}}
			cfg.Routes = []config.RouteConfig{{Name: "test-route", Provider: "mock"}}
			cfg.Providers = []config.ProviderConfig{{Name: "mock", Enabled: true}}
			st := store.NewMemory()
			reg := provider.NewRegistry()
			dcfg := testDispatcherConfig()
			dcfg.PollIntervalMS = 60000 // admission only; no outbound network calls
			d := dispatch.New(nil, reg, nil, dcfg, st)
			defer d.Close()
			d.ReloadRoutes(cfg.Routes, cfg.Providers)
			g := NewWithDispatcher(cfg, st, d)
			body, err := json.Marshal(map[string]string{"from": "brand", "to": "+8613800138000", "text": "hi", "client_msg_id": "same-order", "callback_url": tc.url})
			if err != nil {
				t.Fatal(err)
			}
			var id string
			for attempt := 0; attempt < 2; attempt++ {
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				addClientAuth(req)
				rec := httptest.NewRecorder()
				g.Handler().ServeHTTP(rec, req)
				want := http.StatusBadRequest
				if tc.accepted {
					want = http.StatusAccepted
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d: %s", rec.Code, want, rec.Body.String())
				}
				if tc.accepted {
					var result struct {
						GatewayID string `json:"gateway_id"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.GatewayID == "" {
						t.Fatalf("invalid accepted response: %s", rec.Body.String())
					}
					if attempt > 0 && id != result.GatewayID {
						t.Fatal("callback protocol changed request idempotency")
					}
					id = result.GatewayID
				}
			}
			messages, err := st.ListMessages(context.Background())
			want := 0
			if tc.accepted {
				want = 1
			}
			if err != nil || len(messages) != want {
				t.Fatalf("stored messages=%d want=%d err=%v", len(messages), want, err)
			}
		})
	}
}
