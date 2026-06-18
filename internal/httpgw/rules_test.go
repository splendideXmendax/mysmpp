package httpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	enableClientAuth(&cfg)
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
	addClientAuth(req)
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

func TestMessagesGETUsesPagination(t *testing.T) {
	st := store.NewMemory()
	for i := 0; i < 3; i++ {
		msg := message.New(string(rune('a'+i)), message.DirectionMT, "1069", "13800138000", "hello")
		if err := st.SaveMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	enableClientAuth(&cfg)
	gateway := New(cfg, st)
	req := httptest.NewRequest(http.MethodGet, "/v1/messages?limit=1&offset=1", nil)
	addClientAuth(req)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ID":"b"`) || strings.Contains(rec.Body.String(), `"ID":"a"`) || strings.Contains(rec.Body.String(), `"ID":"c"`) {
		t.Fatalf("pagination not applied: %s", rec.Body.String())
	}
}

func TestMessageSubmitUsesDispatcherWhenConfigured(t *testing.T) {
	cfg := config.Default()
	enableClientAuth(&cfg)
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
	st := store.NewMemory()
	dispatcher := dispatch.New(nil, reg, nil, testDispatcherConfig(), st)
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)

	gateway := NewWithDispatcher(cfg, st, dispatcher)
	body := strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	addClientAuth(req)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"provider":"mock-a"`) {
		t.Fatalf("dispatcher receipt not returned: %s", rec.Body.String())
	}
	waitForMessages(t, st, 1)
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Direction != message.DirectionMT || messages[0].Provider != "mock-a" {
		t.Fatalf("dispatcher submit should store MT message: %+v", messages)
	}
}

func TestMessagesGETRequiresConfiguredClient(t *testing.T) {
	cfg := config.Default()
	cfg.Clients = []config.ClientAuth{{
		ClientID: "client-a",
		Token:    "token-a",
		Enabled:  true,
	}}
	gateway := New(cfg, store.NewMemory())

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without client credentials, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.Header.Set("X-Client-ID", "client-a")
	req.Header.Set("X-Token", "token-a")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with client credentials, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageSubmitRequiresConfiguredClient(t *testing.T) {
	cfg := config.Default()
	cfg.Clients = []config.ClientAuth{{
		ClientID: "client-a",
		Token:    "token-a",
		Enabled:  true,
	}}
	gateway := New(cfg, store.NewMemory())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without client credentials, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", "client-a")
	req.Header.Set("X-Token", "token-a")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with client credentials, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageSubmitRejectsInvalidClientMsgID(t *testing.T) {
	cfg := config.Default()
	enableClientAuth(&cfg)
	gateway := New(cfg, store.NewMemory())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello","client_msg_id":"bad id"}`))
	req.Header.Set("Content-Type", "application/json")
	addClientAuth(req)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for client_msg_id with spaces, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthReportsStorageErrors(t *testing.T) {
	gateway := New(config.Default(), failingStore{err: errors.New("storage down")})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"unhealthy"`) || !strings.Contains(rec.Body.String(), "storage down") {
		t.Fatalf("health did not expose storage failure: %s", rec.Body.String())
	}
}

func TestMessageSubmitIdempotencyReturnsSameGatewayID(t *testing.T) {
	cfg := config.Default()
	enableClientAuth(&cfg)
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
	st := store.NewMemory()
	dispatcher := dispatch.New(nil, reg, nil, testDispatcherConfig(), st)
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)
	gateway := NewWithDispatcher(cfg, st, dispatcher)

	body := `{"from":"1069","to":"13800138000","text":"hello","client_msg_id":"m-1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addClientAuth(req)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var first dispatch.Receipt
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addClientAuth(req)
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var second dispatch.Receipt
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if first.GatewayID != second.GatewayID {
		t.Fatalf("expected idempotent gateway id, got %q and %q", first.GatewayID, second.GatewayID)
	}
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one stored message, got %d", len(messages))
	}
}

func TestMessageSubmitRiskBlockedKeywordStoresBlockedMessage(t *testing.T) {
	cfg := config.Default()
	enableClientAuth(&cfg)
	cfg.Risk.BlockedKeywords = []string{"spam"}
	st := store.NewMemory()
	gateway := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"1069","to":"13800138000","text":"buy spam now"}`))
	req.Header.Set("Content-Type", "application/json")
	addClientAuth(req)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	messages, err := st.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].State != "blocked" || messages[0].Metadata["reason"] != "blocked_keyword" || messages[0].Metadata["client_id"] != "client-a" {
		t.Fatalf("expected blocked message record, got %+v", messages)
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
		Name:       "dlr",
		Method:     "POST",
		Path:       "/callback/dlr",
		Provider:   "mock-a",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
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
	st := store.NewMemory()
	dispatcher := dispatch.New(nil, reg, nil, testDispatcherConfig(), st)
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)

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
	waitForPending(t, dispatcher, 1)
	if dispatcher.PendingSize() != 1 {
		t.Fatalf("expected pending before dlr, got %d", dispatcher.PendingSize())
	}
	msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || msg.ProviderID == "" {
		t.Fatalf("expected sent message with provider id, got ok=%v msg=%+v", ok, msg)
	}

	body := strings.NewReader(`{"providerId":"` + msg.ProviderID + `","state":"DELIVRD","err":"0"}`)
	req := httptest.NewRequest(http.MethodPost, "/callback/dlr", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", "secret")
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
	if len(messages) != 1 || messages[0].State != "DELIVRD" {
		t.Fatalf("dlr callback should update MT message state: %+v", messages)
	}
}

func TestDynamicInboundRuleRejectsDLRProviderMismatch(t *testing.T) {
	cfg := config.Default()
	cfg.Inbound = []config.HTTPRuleConfig{{
		Name:       "dlr",
		Method:     "POST",
		Path:       "/callback/dlr",
		Provider:   "mock-b",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
		Fields: map[string]string{
			"provider_id": "providerId",
			"status":      "state",
		},
	}}
	st := store.NewMemory()
	msg := message.New("g1", message.DirectionMT, "1069", "13800138000", "hello")
	msg.Provider = "mock-a"
	msg.ProviderID = "p1"
	msg.State = "sent"
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePending(context.Background(), store.Pending{
		ProviderID: "p1",
		GatewayID:  "g1",
		Provider:   "mock-a",
		SourceKind: dispatch.SourceHTTPAPI.String(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := dispatch.New(nil, nil, nil, testDispatcherConfig(), st)
	defer dispatcher.Close()
	gateway := NewWithDispatcher(cfg, st, dispatcher)

	req := httptest.NewRequest(http.MethodPost, "/callback/dlr", strings.NewReader(`{"providerId":"p1","state":"DELIVRD"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Token", "secret")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if dispatcher.PendingSize() != 1 {
		t.Fatalf("provider mismatch should keep pending record, got %d", dispatcher.PendingSize())
	}
	got, ok, err := st.GetMessage(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.State != "sent" {
		t.Fatalf("provider mismatch should not update message state: ok=%v msg=%+v", ok, got)
	}
}

func TestConfigAPIUpdatesRuntimeConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	st := store.NewMemory()
	gateway := New(cfg, st)
	cfg.Inbound = []config.HTTPRuleConfig{{
		Name:       "new-callback",
		Method:     "POST",
		Path:       "/callback/new",
		AuthHeader: "X-Token",
		AuthToken:  "secret",
		Fields:     map[string]string{"from": "src", "to": "dst", "text": "msg"},
	}}

	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(payload))
	req.SetBasicAuth("admin", "secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/callback/new", strings.NewReader(`src=10010&dst=13800138000&msg=ok`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Token", "secret")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dynamic inbound to work, got %d", rec.Code)
	}
}

func TestConfigAPIRequiresAdminWhenUnconfigured(t *testing.T) {
	gateway := New(config.Default(), store.NewMemory())
	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestConfigAPIRequiresBasicAuthWhenConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	gateway := New(cfg, store.NewMemory())

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.SetBasicAuth("admin", "secret")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with credentials, got %d", rec.Code)
	}
}

func TestConfigAPIReloadsDispatcherRoutesAndProviders(t *testing.T) {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	cfg.Routes = []config.RouteConfig{{
		Name:     "old",
		Provider: "mock-a",
		Priority: 1,
	}}
	cfg.Providers = []config.ProviderConfig{{
		Name:     "mock-a",
		Protocol: "mock",
		Enabled:  true,
	}}
	reg := provider.NewRegistry()
	reg.Replace(provider.BuildProviders(context.Background(), cfg))
	defer reg.CloseAll()
	dispatcher := dispatch.New(nil, reg, nil, testDispatcherConfig(), store.NewMemory())
	defer dispatcher.Close()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)

	st := store.NewMemory()
	gateway := NewWithDispatcher(cfg, st, dispatcher, reg, context.Background())
	cfg.Routes = []config.RouteConfig{{
		Name:     "new",
		Provider: "mock-b",
		Priority: 1,
	}}
	cfg.Providers = []config.ProviderConfig{{
		Name:     "mock-b",
		Protocol: "mock",
		Enabled:  true,
	}}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(payload))
	req.SetBasicAuth("admin", "secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"1069","to":"13800138000","text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "secret")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"provider":"mock-b"`) || !strings.Contains(rec.Body.String(), `"route":"new"`) {
		t.Fatalf("dispatcher did not use reloaded config: %s", rec.Body.String())
	}
}

func TestConfigAPIPersistsConfigWhenPathConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	cfg.Routes = []config.RouteConfig{{
		Name:     "old",
		Provider: "mock-a",
		Priority: 1,
	}}
	cfg.Providers = []config.ProviderConfig{{
		Name:     "mock-a",
		Protocol: "mock",
		Enabled:  true,
	}}
	configPath := t.TempDir() + "/config.json"
	gateway := NewWithDispatcher(cfg, store.NewMemory(), nil, configPath)

	cfg.Routes = []config.RouteConfig{{
		Name:     "persisted",
		Provider: "mock-a",
		Priority: 9,
	}}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(payload))
	req.SetBasicAuth("admin", "secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Routes) != 1 || loaded.Routes[0].Name != "persisted" {
		t.Fatalf("config api did not persist route: %+v", loaded.Routes)
	}
}

func waitForMessages(t *testing.T, st store.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := st.ListMessages(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	messages, _ := st.ListMessages(context.Background())
	t.Fatalf("message count did not reach %d, got %d", want, len(messages))
}

func waitForPending(t *testing.T, d *dispatch.Dispatcher, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.PendingSize() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending size did not reach %d, got %d", want, d.PendingSize())
}

func TestRequestIPAllowedUsesTrustedProxyXForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.10")

	if !requestIPAllowed(req, []string{"203.0.113.5/32"}, []string{"10.0.0.0/8"}) {
		t.Fatal("expected forwarded client ip to match allowed list")
	}
	if requestIPAllowed(req, []string{"203.0.113.5/32"}, nil) {
		t.Fatal("untrusted proxy must not be allowed to spoof forwarded client ip")
	}
}

func TestMessagesRequireAdminWhenNoClientsConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	gateway := New(cfg, store.NewMemory())

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without admin credentials, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.SetBasicAuth("admin", "secret")
	rec = httptest.NewRecorder()
	gateway.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with admin credentials, got %d: %s", rec.Code, rec.Body.String())
	}
}

func enableClientAuth(cfg *config.Config) {
	cfg.Clients = []config.ClientAuth{{
		ClientID: "client-a",
		Token:    "token-a",
		Enabled:  true,
	}}
}

func addClientAuth(req *http.Request) {
	req.Header.Set("X-Client-ID", "client-a")
	req.Header.Set("X-Token", "token-a")
}

func testDispatcherConfig() config.DispatcherConfig {
	return config.DispatcherConfig{
		Workers:              1,
		PerWorkerConcurrency: 1,
		ClaimLimit:           10,
		PollIntervalMS:       10,
		PendingTTL:           "1m",
		MaxAttempts:          5,
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

type failingStore struct {
	err error
}

func (s failingStore) Ping(context.Context) error { return s.err }
func (s failingStore) SaveMessage(context.Context, message.Message) error {
	return s.err
}
func (s failingStore) GetMessage(context.Context, string) (message.Message, bool, error) {
	return message.Message{}, false, s.err
}
func (s failingStore) UpdateMessageSent(context.Context, string, string) error { return s.err }
func (s failingStore) UpdateMessageState(context.Context, string, string, int) error {
	return s.err
}
func (s failingStore) ListMessages(context.Context) ([]message.Message, error) { return nil, s.err }
func (s failingStore) ListMessagesPage(context.Context, store.ListOptions) ([]message.Message, error) {
	return nil, s.err
}
func (s failingStore) SavePending(context.Context, store.Pending) error { return s.err }
func (s failingStore) GetPending(context.Context, string) (store.Pending, bool, error) {
	return store.Pending{}, false, s.err
}
func (s failingStore) MarkDLRReady(context.Context, string, string, int, time.Time) error {
	return s.err
}
func (s failingStore) ListReadyDLR(context.Context, string, int) ([]store.Pending, error) {
	return nil, s.err
}
func (s failingStore) DeletePending(context.Context, string) error { return s.err }
func (s failingStore) SweepExpiredPending(context.Context, time.Time) (int, error) {
	return 0, s.err
}
func (s failingStore) PendingSize(context.Context) (int, error) { return 0, s.err }
func (s failingStore) ReserveGatewayIDRange(context.Context, uint64) (uint64, uint64, error) {
	return 0, 0, s.err
}
func (s failingStore) EnqueueOutbox(context.Context, store.OutboxItem) (int64, error) {
	return 0, s.err
}
func (s failingStore) ClaimOutbox(context.Context, string, int) ([]store.OutboxItem, error) {
	return nil, s.err
}
func (s failingStore) RequeueStaleOutbox(context.Context, time.Time, int) (int, error) {
	return 0, s.err
}
func (s failingStore) AckOutbox(context.Context, int64) error { return s.err }
func (s failingStore) FailOutbox(context.Context, int64, string, time.Time) error {
	return s.err
}
func (s failingStore) OutboxDepth(context.Context, string) (int, error) { return 0, s.err }
func (s failingStore) CheckIdempotency(context.Context, string, string) (string, bool, error) {
	return "", false, s.err
}
func (s failingStore) SaveIdempotency(context.Context, string, string, string, time.Duration) error {
	return s.err
}
func (s failingStore) SubmitAtomic(context.Context, message.Message, store.OutboxItem, string, string, time.Duration) (int64, error) {
	return 0, s.err
}
