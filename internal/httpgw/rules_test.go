package httpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func TestMessageSubmitAppliesRoute(t *testing.T) {
	cfg := config.Default()
	cfg.Routes = []config.RouteConfig{{
		Name:     "mobile",
		Prefix:   []string{"138"},
		Provider: "cmcc",
		Priority: 10,
	}}
	st := store.NewMemory()
	gateway := New(cfg, st)

	body := strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	if messages[0].Route != "mobile" || messages[0].Provider != "cmcc" {
		t.Fatalf("route not applied: %+v", messages[0])
	}
}

func TestMessageSubmitUsesDispatcherWhenConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Routes = []config.RouteConfig{{
		Name:     "default",
		Provider: "mock-a",
		Priority: 1,
	}}
	cfg.Providers = []config.ProviderConfig{{
		Name:    "mock-a",
		Enabled: true,
	}}
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()
	dispatcher := dispatch.New(nil, reg, nil, time.Minute)
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)

	gateway := NewWithDispatcher(cfg, store.NewMemory(), dispatcher)
	body := strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"provider":"mock-a"`) {
		t.Fatalf("dispatcher receipt not returned: %s", rec.Body.String())
	}
}

func TestDynamicInboundRuleStoresMessage(t *testing.T) {
	cfg := config.Default()
	cfg.Inbound = []config.HTTPRuleConfig{{
		Name:        "partner",
		Method:      "POST",
		Path:        "/callback/partner",
		AuthHeader:  "X-Token",
		AuthToken:   "secret",
		Fields:      map[string]string{"id": "id", "from": "src", "to": "dst", "text": "msg"},
		SuccessBody: `{"ok":true}`,
	}}
	st := store.NewMemory()
	gateway := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/callback/partner", strings.NewReader(`{"id":"m1","src":"10086","dst":"13800138000","msg":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", "secret")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "m1" || messages[0].Text != "hi" {
		t.Fatalf("unexpected stored messages: %+v", messages)
	}
}

func TestDynamicInboundRuleCanHandleProviderDLR(t *testing.T) {
	cfg := config.Default()
	cfg.Routes = []config.RouteConfig{{
		Name:     "default",
		Provider: "mock-a",
		Priority: 1,
	}}
	cfg.Providers = []config.ProviderConfig{{
		Name:    "mock-a",
		Enabled: true,
	}}
	cfg.Inbound = []config.HTTPRuleConfig{{
		Name:   "dlr",
		Method: "POST",
		Path:   "/callback/dlr",
		Fields: map[string]string{
			"provider_id": "providerId",
			"status":      "state",
			"error_code":  "err",
		},
	}}
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()
	dispatcher := dispatch.New(nil, reg, nil, time.Minute)
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)

	st := store.NewMemory()
	gateway := NewWithDispatcher(cfg, st, dispatcher)
	receipt, err := dispatcher.Submit(context.Background(), dispatch.Envelope{
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Source:             dispatch.SubmitSource{Kind: dispatch.SourceHTTPAPI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.PendingSize() != 1 {
		t.Fatalf("expected pending before dlr, got %d", dispatcher.PendingSize())
	}

	body := strings.NewReader(`{"providerId":"` + receipt.ProviderID + `","state":"DELIVRD","err":"0"}`)
	req := httptest.NewRequest(http.MethodPost, "/callback/dlr", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if dispatcher.PendingSize() != 0 {
		t.Fatalf("expected dlr to complete pending record, got %d", dispatcher.PendingSize())
	}
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("dlr callback should not store MO message: %+v", messages)
	}
}

func TestConfigAPIUpdatesRuntimeConfig(t *testing.T) {
	cfg := config.Default()
	st := store.NewMemory()
	gateway := New(cfg, st)
	cfg.Inbound = []config.HTTPRuleConfig{{
		Name:   "new-callback",
		Method: "POST",
		Path:   "/callback/new",
		Fields: map[string]string{"from": "src", "to": "dst", "text": "msg"},
	}}

	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/callback/new", strings.NewReader(`src=10010&dst=13800138000&msg=ok`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dynamic inbound to work, got %d", rec.Code)
	}
}

func TestBuildOutboundRequestJSON(t *testing.T) {
	msg := message.New("m1", message.DirectionMT, "1069", "13800138000", "hello")
	rule := config.HTTPRuleConfig{
		Method:      "POST",
		ContentType: "application/json",
		Fields:      map[string]string{"mobile": "to", "msg": "text"},
		Headers:     map[string]string{"X-Message-ID": "{{id}}"},
	}

	req, err := BuildOutboundRequest(context.Background(), "https://sms.example/send", rule, msg)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Message-ID") != "m1" {
		t.Fatalf("header not expanded: %s", req.Header.Get("X-Message-ID"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"mobile":"13800138000"`) {
		t.Fatalf("unexpected body %s", body)
	}
}
