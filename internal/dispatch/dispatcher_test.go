package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/cdr"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/filter"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type fakeSMPPServer struct {
	session   *smpp.Session
	receivers []*smpp.Session
}

type multiIDProvider struct {
	ids []string
}

type captureProvider struct {
	messages chan provider.OutboundMessage
}

type errorProvider struct {
	err error
}

type countingProvider struct {
	mu    sync.Mutex
	calls int
	id    string
	err   error
}

func (p captureProvider) Send(msg provider.OutboundMessage) (string, error) {
	p.messages <- msg
	return "provider-id", nil
}

func (p captureProvider) OnDLR(provider.DLRCallback) {}

func (p errorProvider) Send(provider.OutboundMessage) (string, error) {
	return "", p.err
}

func (p errorProvider) OnDLR(provider.DLRCallback) {}

func (p *countingProvider) Send(provider.OutboundMessage) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.id, p.err
}

func (p *countingProvider) OnDLR(provider.DLRCallback) {}

func (p *countingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p multiIDProvider) Send(provider.OutboundMessage) (string, error) {
	if len(p.ids) == 0 {
		return "", nil
	}
	return p.ids[0], nil
}

func (p multiIDProvider) SendAll(provider.OutboundMessage) ([]string, error) {
	return append([]string(nil), p.ids...), nil
}

func (p multiIDProvider) OnDLR(provider.DLRCallback) {}

type failCompleteStore struct {
	*store.MemoryStore
}

type ambiguousCompleteStore struct {
	*store.MemoryStore
	mu    sync.Mutex
	calls int
}

type captureCDR struct {
	mu     sync.Mutex
	events []cdr.Event
}

func (s *captureCDR) Emit(e cdr.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *captureCDR) Close() error { return nil }

func (s *captureCDR) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (s failCompleteStore) CompleteOutboxSend(context.Context, int64, string, []store.Pending) error {
	return errors.New("boom")
}

func (s *ambiguousCompleteStore) CompleteOutboxSend(ctx context.Context, id int64, workerID string, pending []store.Pending) error {
	err := s.MemoryStore.CompleteOutboxSend(ctx, id, workerID, pending)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 && err == nil {
		return errors.New("commit result lost")
	}
	return err
}

func (s fakeSMPPServer) Session(id string) (*smpp.Session, bool) {
	if s.session != nil && s.session.ID() == id {
		return s.session, true
	}
	return nil, false
}

func (s fakeSMPPServer) ReceiversBySystemID(systemID string) []*smpp.Session {
	return append([]*smpp.Session(nil), s.receivers...)
}

func TestDispatcherSharesDailyQuotaAcrossHTTPAndSMPPAccounts(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	d.ReloadTenants(config.Config{
		Tenants: []config.TenantConfig{{TenantID: "customer-a", Limits: config.TenantLimits{DailySegments: 2, Timezone: "Asia/Shanghai"}}},
		Clients: []config.ClientAuth{{ClientID: "http-a", TenantID: "customer-a"}},
		ESMEs:   []config.ESMECred{{SystemID: "smpp-a", TenantID: "customer-a"}},
	})

	receipt, err := d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: strings.Repeat("a", 161), ClientID: "http-a", ClientMsgID: "order-1",
		Source: SubmitSource{Kind: SourceHTTPAPI},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
	if err != nil || !ok {
		t.Fatalf("message missing: ok=%v err=%v", ok, err)
	}
	if msg.TenantID != "customer-a" || msg.AccountID != "http-a" || msg.ClientMsgID != "order-1" || len(msg.Segments) != 2 {
		t.Fatalf("tenant identity or segment count not persisted: %+v", msg)
	}
	_, err = d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: "hello", ClientID: "smpp:smpp-a", ClientMsgID: "smpp-request-1",
		Source: SubmitSource{Kind: SourceSMPP, SMPPSystemID: "smpp-a"},
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("shared tenant quota was not enforced for SMPP: %v", err)
	}
}

func TestDispatcherRateLimitReturnsExistingIdempotentReceipt(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"mock-a": multiIDProvider{ids: []string{"up-1"}}})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	d.ReloadTenants(config.Config{
		Tenants: []config.TenantConfig{{TenantID: "customer-a", Limits: config.TenantLimits{TPS: 1, Burst: 1}}},
		Clients: []config.ClientAuth{{ClientID: "http-a", TenantID: "customer-a"}},
		ESMEs:   []config.ESMECred{{SystemID: "smpp-a", TenantID: "customer-a"}},
	})
	env := Envelope{From: "1069", To: "8613800138000", Text: "hello", ClientID: "http-a", ClientMsgID: "order-1", Source: SubmitSource{Kind: SourceHTTPAPI}}
	first, err := d.Submit(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := d.Submit(context.Background(), env)
	if err != nil || duplicate.GatewayID != first.GatewayID {
		t.Fatalf("rate-limited duplicate did not return original receipt: first=%+v duplicate=%+v err=%v", first, duplicate, err)
	}
	smppEnv := Envelope{
		From: "1069", To: "8613800138000", Text: "hello", ClientID: "smpp:smpp-a", ClientMsgID: "smpp-order-2",
		Source: SubmitSource{Kind: SourceSMPP, SMPPSystemID: "smpp-a"},
	}
	if _, err := d.Submit(context.Background(), smppEnv); !errors.Is(err, ErrRateExceeded) {
		t.Fatalf("new SMPP work should share the HTTP tenant rate limit, got %v", err)
	}
}

func TestDispatcherAggregatesOutOfOrderSegmentDLRs(t *testing.T) {
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), provider.NewRegistry(), nil, testDispatcherConfig(), st)
	defer d.Close()
	msg := testMessage("g-segments")
	msg.State = "sent"
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := st.SavePending(context.Background(), store.Pending{
			Provider: "mock-a", ProviderID: "up-" + string(rune('0'+i)), GatewayID: msg.ID,
			SegmentIndex: i, SegmentCount: 3, SourceKind: SourceHTTPAPI.String(),
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, dlr := range []provider.DLR{
		{Provider: "mock-a", ProviderID: "up-2", State: "UNDELIV", ErrorCode: 42},
		{Provider: "mock-a", ProviderID: "up-1", State: "DELIVRD"},
	} {
		if err := d.HandleDLR(context.Background(), dlr); err != nil {
			t.Fatal(err)
		}
		got, _, _ := st.GetMessage(context.Background(), msg.ID)
		if got.State != "sent" {
			t.Fatalf("message became final before all segments completed: %+v", got)
		}
	}
	if err := d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "up-3", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := st.GetMessage(context.Background(), msg.ID)
	if got.State != "UNDELIV" || got.ErrorCode != 42 {
		t.Fatalf("unexpected aggregate final state: %+v", got)
	}
	segments, err := st.ListPendingByGatewayID(context.Background(), msg.ID)
	if err != nil || len(segments) != 0 {
		t.Fatalf("completed segment mappings were not cleaned up: count=%d err=%v", len(segments), err)
	}
}

func TestDispatcherHTTPCallbackIncludesSegmentAggregation(t *testing.T) {
	payloads := make(chan map[string]any, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode callback: %v", err)
		}
		payloads <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), provider.NewRegistry(), nil, testDispatcherConfig(), st)
	d.httpClient = srv.Client()
	defer d.Close()
	msg := testMessage("g-callback-segments")
	if err := st.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := st.SavePending(context.Background(), store.Pending{
			Provider: "mock-a", ProviderID: "cb-" + string(rune('0'+i)), GatewayID: msg.ID,
			ClientMsgID: "order-42", SegmentIndex: i, SegmentCount: 2, SourceKind: SourceHTTPAPI.String(),
			CallbackURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "cb-2", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	payload := <-payloads
	if payload["client_msg_id"] != "order-42" || payload["segment_index"] != float64(2) || payload["segment_count"] != float64(2) {
		t.Fatalf("callback segment metadata missing: %+v", payload)
	}
	if payload["message_state"] != "PENDING" || payload["final"] != false {
		t.Fatalf("callback aggregate state is incorrect: %+v", payload)
	}
}

func TestDispatcherRoutesAndSubmits(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()

	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "mobile",
		Prefix:   []string{"138"},
		Provider: "mock-a",
		Priority: 10,
	}}, []config.ProviderConfig{{
		Name:    "mock-a",
		Enabled: true,
	}})

	receipt, err := d.Submit(context.Background(), Envelope{
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Source:             SubmitSource{Kind: SourceHTTPAPI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GatewayID != "m0000001" || receipt.Provider != "mock-a" || receipt.Route != "mobile" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	waitForPending(t, d, 1)
	if d.PendingSize() != 1 {
		t.Fatalf("expected one pending record, got %d", d.PendingSize())
	}
}

func TestGatewayIDFitsVendorCOctetLimit(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	receipt, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.GatewayID) != 8 || len(smpp.CString(receipt.GatewayID)) != 9 {
		t.Fatalf("gateway id does not fit Var. Max 9 C-Octet String: id=%q visible=%d encoded=%d", receipt.GatewayID, len(receipt.GatewayID), len(smpp.CString(receipt.GatewayID)))
	}
}

func TestDispatcherWeightedRoutingUsesGatewayID(t *testing.T) {
	reg := provider.NewRegistry()
	mockA := provider.NewNamedMock(context.Background(), "mock-a")
	mockB := provider.NewNamedMock(context.Background(), "mock-b")
	reg.Replace(map[string]provider.Provider{"mock-a": mockA, "mock-b": mockB})
	defer reg.CloseAll()

	providers := []config.ProviderConfig{{Name: "mock-a", Enabled: true}, {Name: "mock-b", Enabled: true}}
	routes := []config.RouteConfig{{
		Name: "weighted",
		Weighted: []config.WeightedProvider{
			{Provider: "mock-a", Weight: 1},
			{Provider: "mock-b", Weight: 1},
		},
		Priority: 1,
	}}
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.ReloadRoutes(routes, providers)

	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		receipt, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(receipt.GatewayID))
		expectedProvider := "mock-a"
		if h.Sum64()%2 == 1 {
			expectedProvider = "mock-b"
		}
		if receipt.Provider != expectedProvider {
			t.Fatalf("gateway id %q selected %q, want %q", receipt.GatewayID, receipt.Provider, expectedProvider)
		}
		seen[receipt.Provider] = true
	}
	if !seen["mock-a"] || !seen["mock-b"] {
		t.Fatalf("same destination was not distributed by gateway id: %v", seen)
	}
}

func TestDispatcherWeightedRoutingPersistsSelectedProvider(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{
		"mock-a": provider.NewNamedMock(context.Background(), "mock-a"),
		"mock-b": provider.NewNamedMock(context.Background(), "mock-b"),
	})
	defer reg.CloseAll()

	st := store.NewMemory()
	cfg := testDispatcherConfig()
	cfg.PollIntervalMS = 60_000
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name: "weighted",
		Weighted: []config.WeightedProvider{
			{Provider: "mock-a", Weight: 1},
			{Provider: "mock-b", Weight: 1},
		},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}, {Name: "mock-b", Enabled: true}})

	receipts := map[string]Receipt{}
	for i := 0; i < 10; i++ {
		receipt, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		receipts[receipt.GatewayID] = receipt
		msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
		if err != nil || !ok {
			t.Fatalf("message %q not stored: ok=%v err=%v", receipt.GatewayID, ok, err)
		}
		if msg.Provider != receipt.Provider || msg.Route != receipt.Route {
			t.Fatalf("message routing differs from receipt: receipt=%+v message=%+v", receipt, msg)
		}
	}

	items, err := st.ClaimOutbox(context.Background(), "inspect", len(receipts))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(receipts) {
		t.Fatalf("expected %d outbox items, got %d", len(receipts), len(items))
	}
	for _, item := range items {
		receipt, ok := receipts[item.GatewayID]
		if !ok {
			t.Fatalf("unexpected outbox gateway id %q", item.GatewayID)
		}
		if item.Provider != receipt.Provider || item.Payload.Provider != receipt.Provider || item.Payload.GatewayID != receipt.GatewayID {
			t.Fatalf("outbox routing differs from receipt: receipt=%+v item=%+v", receipt, item)
		}
	}
}

func TestDispatcherRejectsUnassignedCountryCode(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "default",
		Prefix:   []string{},
		Provider: "mock-a",
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	_, err := d.Submit(context.Background(), Envelope{From: "1069", To: "285032768252", Text: "hello"})
	if !errors.Is(err, ErrInvalidDestAddr) {
		t.Fatalf("expected invalid destination address, got %v", err)
	}
	if depth, err := st.OutboxDepth(context.Background(), ""); err != nil {
		t.Fatal(err)
	} else if depth != 0 {
		t.Fatalf("invalid destination should not enter outbox, depth=%d", depth)
	}
}

func TestDispatcherRejectedSubmissionsDoNotConsumeGatewayIDs(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()

	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	providers := []config.ProviderConfig{{Name: "mock-a", Enabled: true}}
	d.ReloadRoutes([]config.RouteConfig{{Name: "only-cn", Prefix: []string{"86"}, Provider: "mock-a", Priority: 1}}, providers)
	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "441234567890", Text: "hello"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected no route rejection, got %v", err)
	}
	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "285032768252", Text: "hello"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected prefix rejection before validation, got %v", err)
	}

	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, providers)
	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "285032768252", Text: "hello"}); !errors.Is(err, ErrInvalidDestAddr) {
		t.Fatalf("expected invalid destination rejection, got %v", err)
	}
	receipt, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GatewayID != "m0000001" {
		t.Fatalf("rejected submissions consumed gateway ids, got first accepted id %q", receipt.GatewayID)
	}
}

func TestDispatcherInvalidDestinationCDRKeepsSingleProvider(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()

	sink := &captureCDR{}
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.SetCDRSink(sink)
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "285032768252", Text: "hello"}); !errors.Is(err, ErrInvalidDestAddr) {
		t.Fatalf("expected invalid destination rejection, got %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 {
		t.Fatalf("expected one rejected CDR, got %+v", sink.events)
	}
	event := sink.events[0]
	if event.Kind != "rejected" || event.Reason != "bad_dest" || event.Route != "default" || event.Provider != "mock-a" || event.GatewayID != "" {
		t.Fatalf("unexpected invalid destination CDR: %+v", event)
	}
}

func TestDispatcherRejectsCountrySpecificOversizedNumber(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "default",
		Provider: "mock-a",
		Priority: 1,
		DestAddr: config.DestAddrConfig{CountryLengthMode: "compat"},
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	_, err := d.Submit(context.Background(), Envelope{From: "1069", To: "860015013628000", Text: "hello"})
	if !errors.Is(err, ErrInvalidDestAddr) {
		t.Fatalf("expected country-specific length rejection, got %v", err)
	}
}

func TestDispatcherKeepsCountriesWithoutSpecificLengthRule(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "default",
		Provider: "mock-a",
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "246123456789", Text: "hello"}); err != nil {
		t.Fatalf("expected supplemental E.164 code to remain valid, got %v", err)
	}
}

func TestDispatcherCountryLengthModes(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		to      string
		wantErr bool
	}{
		{name: "compat checks covered country", mode: "compat", to: "860015013628000", wantErr: true},
		{name: "off keeps legacy length behavior", mode: "off", to: "860015013628000"},
		{name: "compat allows uncovered country", mode: "compat", to: "246123456789"},
		{name: "strict rejects uncovered country", mode: "strict", to: "246123456789", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reg := provider.NewRegistry()
			mock := provider.NewNamedMock(context.Background(), "mock-a")
			mock.DelayMin = time.Hour
			mock.DelayMax = time.Hour
			reg.Replace(map[string]provider.Provider{"mock-a": mock})
			defer reg.CloseAll()
			d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
			defer d.Close()
			d.ReloadRoutes([]config.RouteConfig{{
				Name:     "default",
				Provider: "mock-a",
				Priority: 1,
				DestAddr: config.DestAddrConfig{CountryLengthMode: test.mode},
			}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
			_, err := d.Submit(context.Background(), Envelope{From: "1069", To: test.to, Text: "hello"})
			if test.wantErr && !errors.Is(err, ErrInvalidDestAddr) {
				t.Fatalf("expected invalid destination, got %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("expected destination to pass, got %v", err)
			}
		})
	}
}

func TestDispatcherCanRewriteTrunkZeroAfterCountryCode(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()

	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "cn",
		Prefix:   []string{"86"},
		Provider: "mock-a",
		Priority: 1,
		AddrRewrite: config.AddrRewriteConfig{
			StripTrunkZeroAfterCC: true,
			CountryCode:           "86",
			EnforceE164Len:        true,
		},
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	receipt, err := d.Submit(context.Background(), Envelope{From: "1069", To: "860015013628000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
	if err != nil || !ok {
		t.Fatalf("message not saved: ok=%v err=%v", ok, err)
	}
	if msg.To != "8615013628000" {
		t.Fatalf("expected rewritten destination, got %q", msg.To)
	}
}

func TestDispatcherConcurrentIdempotencyQueuesOnce(t *testing.T) {
	reg := provider.NewRegistry()
	mock := provider.NewNamedMock(context.Background(), "mock-a")
	mockB := provider.NewNamedMock(context.Background(), "mock-b")
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	mockB.DelayMin = time.Hour
	mockB.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock, "mock-b": mockB})
	defer reg.CloseAll()

	st := store.NewMemory()
	cfg := testDispatcherConfig()
	cfg.Workers = 1
	cfg.PerWorkerConcurrency = 1
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:   "default",
		Prefix: []string{},
		Weighted: []config.WeightedProvider{
			{Provider: "mock-a", Weight: 1},
			{Provider: "mock-b", Weight: 1},
		},
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}, {Name: "mock-b", Enabled: true}})

	const submissions = 20
	receipts := make(chan Receipt, submissions)
	errs := make(chan error, submissions)
	var wg sync.WaitGroup
	for i := 0; i < submissions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := d.Submit(context.Background(), Envelope{
				From:        "1069",
				To:          "8613800138000",
				Text:        "hello",
				ClientID:    "client-a",
				ClientMsgID: "same-key",
				Source:      SubmitSource{Kind: SourceHTTPAPI},
			})
			if err != nil {
				errs <- err
				return
			}
			receipts <- receipt
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var firstID, firstProvider string
	for receipt := range receipts {
		if firstID == "" {
			firstID = receipt.GatewayID
			firstProvider = receipt.Provider
			continue
		}
		if receipt.GatewayID != firstID || receipt.Provider != firstProvider {
			t.Fatalf("expected duplicate submits to return the same routing, first=%q/%q got=%q/%q", firstID, firstProvider, receipt.GatewayID, receipt.Provider)
		}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(firstID))
	expectedProvider := "mock-a"
	if h.Sum64()%2 == 1 {
		expectedProvider = "mock-b"
	}
	if firstProvider != expectedProvider {
		t.Fatalf("gateway id %q returned provider %q, want %q", firstID, firstProvider, expectedProvider)
	}
	msg, ok, err := st.GetMessage(context.Background(), firstID)
	if err != nil || !ok {
		t.Fatalf("idempotent message not stored: ok=%v err=%v", ok, err)
	}
	if msg.Provider != firstProvider {
		t.Fatalf("stored provider %q differs from receipt provider %q", msg.Provider, firstProvider)
	}
	if depth, err := st.OutboxDepth(context.Background(), ""); err != nil {
		t.Fatal(err)
	} else if depth != 1 {
		t.Fatalf("expected one outbox row, got %d", depth)
	}
}

func TestDispatcherDoesNotEmitAcceptedForDuplicate(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"mock-a": multiIDProvider{ids: []string{"p1"}}})
	st := store.NewMemory()
	cfg := testDispatcherConfig()
	cfg.Workers = 0
	sink := &captureCDR{}
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.SetCDRSink(sink)
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Prefix: []string{}, Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	env := Envelope{From: "1069", To: "8613800138000", Text: "hello", ClientID: "c1", ClientMsgID: "k1", Source: SubmitSource{Kind: SourceHTTPAPI}}
	if _, err := d.Submit(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Submit(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got := sink.count("accepted"); got != 1 {
		t.Fatalf("expected one accepted cdr, got %d events=%+v", got, sink.events)
	}
}

func TestDispatcherBlocksContentForAllSources(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	sink := &captureCDR{}
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.SetCDRSink(sink)
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Prefix: []string{}, Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	engine, err := filter.Compile(config.FilterConfig{
		Enabled:   true,
		Normalize: config.NormalizeConfig{Lowercase: true},
		Rules:     []config.FilterRule{{Name: "blocked", Keywords: []string{"bad"}, Action: "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.SetFilterEngine(engine)
	for _, source := range []SubmitSource{{Kind: SourceHTTPAPI}, {Kind: SourceSMPP, SMPPSystemID: "esme-a"}} {
		_, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "bad text", Source: source})
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("expected ErrBlocked for %s, got %v", source.Kind.String(), err)
		}
	}
	if got := sink.count("rejected"); got != 2 {
		t.Fatalf("expected two rejected cdr events, got %d", got)
	}
}

func TestDispatcherMaskDisablesRawPayloadPassthrough(t *testing.T) {
	reg := provider.NewRegistry()
	captured := make(chan provider.OutboundMessage, 2)
	reg.Replace(map[string]provider.Provider{"mock-a": captureProvider{messages: captured}})
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	engine, err := filter.Compile(config.FilterConfig{
		Enabled: true,
		Rules:   []config.FilterRule{{Name: "mask", Keywords: []string{"bad"}, Action: "mask", MaskWith: "*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.SetFilterEngine(engine)
	if _, err := d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: "bad", DataCoding: 0, Encoding: "gsm7",
		RawPayload: []byte("bad"), RawPayloadSet: true, Source: SubmitSource{Kind: SourceSMPP},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-captured:
		if msg.Text != "*" || msg.RawPayloadSet || msg.RawPayload != nil {
			t.Fatalf("masked message kept raw bytes: %+v", msg)
		}
		if msg.Encoding != "gsm7" {
			t.Fatalf("masked encoding=%q, want gsm7", msg.Encoding)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("masked message was not sent to provider")
	}
}

func TestDispatcherRejectsMaskForMultipartSegment(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"mock-a": multiIDProvider{ids: []string{"p1"}}})
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	engine, err := filter.Compile(config.FilterConfig{
		Enabled: true,
		Rules:   []config.FilterRule{{Name: "mask", Keywords: []string{"bad"}, Action: "mask", MaskWith: "你"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.SetFilterEngine(engine)
	_, err = d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: "bad", DataCoding: 0x03, Encoding: "8bit",
		UDH: []byte{0x05, 0x00, 0x03, 0x12, 0x02, 0x01}, RawPayload: []byte("bad"), RawPayloadSet: true,
		Source: SubmitSource{Kind: SourceSMPP},
	})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected multipart mask to be blocked, got %v", err)
	}
}

func TestDispatcherPreservesRawPayloadToProvider(t *testing.T) {
	reg := provider.NewRegistry()
	captured := make(chan provider.OutboundMessage, 1)
	reg.Replace(map[string]provider.Provider{"mock-a": captureProvider{messages: captured}})
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), store.NewMemory())
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})
	payload := []byte{0x1b, 0x65}
	if _, err := d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", DataCoding: 0, Encoding: "gsm7",
		RawPayload: payload, RawPayloadSet: true, Source: SubmitSource{Kind: SourceSMPP},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-captured:
		if msg.DataCoding != 0 || !msg.RawPayloadSet || string(msg.RawPayload) != string(payload) {
			t.Fatalf("provider message changed: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("raw message was not sent to provider")
	}
}

func TestDispatcherRouteCanDisableDestValidationForShortCode(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	cfg := testDispatcherConfig()
	disabled := false
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "short",
		Prefix:   []string{"123"},
		Provider: "mock-a",
		Priority: 1,
		DestAddr: config.DestAddrConfig{
			Validate: &disabled,
		},
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	_, err := d.Submit(context.Background(), Envelope{From: "1069", To: "123", Text: "hello"})
	if err != nil {
		t.Fatalf("expected short code to pass when route validation is disabled: %v", err)
	}
}

func TestDispatcherDefersSMPPDLRUntilReceiverBound(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, fakeSMPPServer{}, testDispatcherConfig(), st)
	defer d.Close()
	rec := store.Pending{
		ProviderID:         "up-1",
		GatewayID:          "g000000000001",
		SourceKind:         SourceSMPP.String(),
		SourceSession:      "tx-1",
		SourceSystem:       "esme-a",
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Provider:           "mock-a",
		ReceivedAt:         time.Now().UTC(),
		ExpiresAt:          time.Now().Add(time.Hour),
	}
	if err := st.SavePending(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMessage(context.Background(), testMessage(rec.GatewayID)); err != nil {
		t.Fatal(err)
	}

	err := d.HandleDLR(context.Background(), provider.DLR{
		Provider:   "mock-a",
		ProviderID: "up-1",
		State:      "DELIVRD",
		DoneAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, ok, err := st.GetPending(context.Background(), "mock-a", "up-1")
	if err != nil || !ok {
		t.Fatalf("pending not kept: ok=%v err=%v", ok, err)
	}
	if !pending.DLRReady || pending.DLRState != "DELIVRD" {
		t.Fatalf("dlr not marked ready: %+v", pending)
	}
}

func TestDispatcherWaitsForPendingWhenDLRArrivesEarly(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()

	const providerID = "up-fast-1"
	const gatewayID = "g000000000001"
	if err := st.SaveMessage(context.Background(), testMessage(gatewayID)); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = st.SavePending(context.Background(), store.Pending{
			ProviderID:         providerID,
			GatewayID:          gatewayID,
			SourceKind:         SourceHTTPAPI.String(),
			From:               "1069",
			To:                 "13800138000",
			Text:               "hello",
			RegisteredDelivery: 1,
			Provider:           "mock-a",
			ReceivedAt:         time.Now().UTC(),
			ExpiresAt:          time.Now().Add(time.Hour),
		})
	}()

	if err := d.HandleDLR(context.Background(), provider.DLR{
		Provider:   "mock-a",
		ProviderID: providerID,
		State:      "DELIVRD",
		DoneAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	msg, ok, err := st.GetMessage(context.Background(), gatewayID)
	if err != nil || !ok {
		t.Fatalf("message not found after dlr: ok=%v err=%v", ok, err)
	}
	if msg.State != "DELIVRD" {
		t.Fatalf("message state = %q", msg.State)
	}
	if _, ok, err := st.GetPending(context.Background(), "mock-a", providerID); err != nil || ok {
		t.Fatalf("pending should be deleted after http dlr: ok=%v err=%v", ok, err)
	}
}

func TestDispatcherKeepsPendingForIntermediateDLR(t *testing.T) {
	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	rec := store.Pending{
		ProviderID:         "up-1",
		GatewayID:          "g000000000001",
		SourceKind:         SourceHTTPAPI.String(),
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Provider:           "mock-a",
		ReceivedAt:         time.Now().UTC(),
		ExpiresAt:          time.Now().Add(time.Hour),
	}
	if err := st.SaveMessage(context.Background(), testMessage(rec.GatewayID)); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePending(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "up-1", State: "ENROUTE"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetPending(context.Background(), "mock-a", "up-1"); err != nil || !ok {
		t.Fatalf("pending should remain for intermediate dlr: ok=%v err=%v", ok, err)
	}
	if err := d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "up-1", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetPending(context.Background(), "mock-a", "up-1"); err != nil || ok {
		t.Fatalf("pending should be deleted for final dlr: ok=%v err=%v", ok, err)
	}
}

func TestDispatcherSendsHTTPCallback(t *testing.T) {
	calls := make(chan struct{}, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("callback method = %s", r.Method)
		}
		calls <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reg := provider.NewRegistry()
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	d.httpClient = srv.Client()
	defer d.Close()
	rec := store.Pending{
		ProviderID:   "up-1",
		GatewayID:    "g000000000001",
		SourceKind:   SourceHTTPAPI.String(),
		CallbackURL:  srv.URL,
		CallbackRule: "rule-a",
		From:         "1069",
		To:           "13800138000",
		Text:         "hello",
		Provider:     "mock-a",
		ReceivedAt:   time.Now().UTC(),
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := st.SaveMessage(context.Background(), testMessage(rec.GatewayID)); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePending(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "up-1", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("callback was not sent")
	}
}

func TestDispatcherCreatesPendingForEachProviderID(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"multi": multiIDProvider{ids: []string{"p1", "p2", "p3"}}})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Prefix: []string{}, Provider: "multi", Priority: 1}}, []config.ProviderConfig{{Name: "multi", Enabled: true}})

	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello", Source: SubmitSource{Kind: SourceHTTPAPI}}); err != nil {
		t.Fatal(err)
	}
	waitForPending(t, d, 3)
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, ok, err := st.GetPending(context.Background(), "multi", id); err != nil || !ok {
			t.Fatalf("pending %s missing: ok=%v err=%v", id, ok, err)
		}
	}
}

func TestDispatcherDoesNotRequeueWhenCompletionFailsAfterSend(t *testing.T) {
	reg := provider.NewRegistry()
	upstream := &countingProvider{id: "p1"}
	reg.Replace(map[string]provider.Provider{"mock-a": upstream})
	st := failCompleteStore{MemoryStore: store.NewMemory()}
	cfg := testDispatcherConfig()
	cfg.MaxAttempts = 3
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Prefix: []string{}, Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello", Source: SubmitSource{Kind: SourceHTTPAPI}}); err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "uncertain", 1)
	if count := upstream.callCount(); count != 1 {
		t.Fatalf("provider calls = %d, want 1", count)
	}
	if n, err := st.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("uncertain outbox must not be requeued: count=%d err=%v", n, err)
	}
}

func TestDispatcherRetriesCompletionWithoutResending(t *testing.T) {
	reg := provider.NewRegistry()
	upstream := &countingProvider{id: "p1"}
	reg.Replace(map[string]provider.Provider{"mock-a": upstream})
	st := &ambiguousCompleteStore{MemoryStore: store.NewMemory()}
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "mock-a", Priority: 1}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

	if _, err := d.Submit(context.Background(), Envelope{From: "1069", To: "8613800138000", Text: "hello", Source: SubmitSource{Kind: SourceHTTPAPI}}); err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "done", 1)
	if count := upstream.callCount(); count != 1 {
		t.Fatalf("provider calls = %d, want 1", count)
	}
}

func TestDispatcherSendsTerminalFailureDLRToOnlineSMPPClient(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	receiver := smpp.NewSession(serverConn, smpp.SessionConfig{ID: "trx-1", Auth: func(systemID, password string) bool { return true }})
	go receiver.Serve(context.Background())
	bindSessionForDispatchTest(t, clientConn, smpp.CommandBindTransceiver, smpp.CommandBindTransceiverResp)

	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{
		"reject": errorProvider{err: smppclient.PermanentError{Err: smppclient.SubmitStatusError{Status: smpp.StatusInvalidSrcAddr}}},
	})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, fakeSMPPServer{session: receiver, receivers: []*smpp.Session{receiver}}, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "reject", Priority: 1}}, []config.ProviderConfig{{Name: "reject", Enabled: true}})

	receipt, err := d.Submit(context.Background(), Envelope{
		From:               "1069",
		To:                 "8613800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Source: SubmitSource{
			Kind:          SourceSMPP,
			SMPPSessionID: receiver.ID(),
			SMPPSystemID:  "esme-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dlr, err := readPDUForDispatchTest(clientConn, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if dlr.CommandID != smpp.CommandDeliverSM {
		t.Fatalf("expected deliver_sm, got 0x%08x", dlr.CommandID)
	}
	if err := smpp.WritePDU(clientConn, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, Status: smpp.StatusOK, SequenceID: dlr.SequenceID}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(dlr.Body, []byte("id:"+receipt.GatewayID)) || !bytes.Contains(dlr.Body, []byte("dlvrd:000")) || !bytes.Contains(dlr.Body, []byte("stat:REJECTD")) || !bytes.Contains(dlr.Body, []byte("err:010")) {
		t.Fatalf("unexpected failure receipt: %q", dlr.Body)
	}
	waitForOutboxDepth(t, st, "failed", 1)
	waitForPending(t, d, 0)
}

func TestDispatcherPersistsTerminalFailureDLRForOfflineSMPPClient(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{
		"reject": errorProvider{err: smppclient.PermanentError{Err: smppclient.SubmitStatusError{Status: smpp.StatusInvalidSrcAddr}}},
	})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "reject", Priority: 1}}, []config.ProviderConfig{{Name: "reject", Enabled: true}})

	receipt, err := d.Submit(context.Background(), Envelope{
		From:               "1069",
		To:                 "8613800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Source:             SubmitSource{Kind: SourceSMPP, SMPPSessionID: "tx-1", SMPPSystemID: "esme-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "failed", 1)
	waitForPending(t, d, 1)
	items, err := st.ListReadyDLR(context.Background(), "esme-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("ready dlr count = %d, want 1", len(items))
	}
	if items[0].GatewayID != receipt.GatewayID || items[0].ProviderID != "local-failure:"+receipt.GatewayID || items[0].DLRState != "REJECTD" || items[0].DLRErrorCode != int(smpp.StatusInvalidSrcAddr) {
		t.Fatalf("unexpected pending failure dlr: %+v", items[0])
	}
	msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
	if err != nil || !ok {
		t.Fatalf("message missing: ok=%v err=%v", ok, err)
	}
	if msg.State != "REJECTD" || msg.ErrorCode != int(smpp.StatusInvalidSrcAddr) {
		t.Fatalf("unexpected failed message: %+v", msg)
	}
}

func TestDispatcherSkipsTerminalFailureDLRWhenNotRequested(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{
		"reject": errorProvider{err: smppclient.PermanentError{Err: smppclient.SubmitStatusError{Status: smpp.StatusInvalidSrcAddr}}},
	})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "reject", Priority: 1}}, []config.ProviderConfig{{Name: "reject", Enabled: true}})

	if _, err := d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: "hello",
		Source: SubmitSource{Kind: SourceSMPP, SMPPSessionID: "tx-1", SMPPSystemID: "esme-a"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "failed", 1)
	if size := d.PendingSize(); size != 0 {
		t.Fatalf("pending size = %d, want 0", size)
	}
}

func TestDispatcherDoesNotRetryAmbiguousProviderError(t *testing.T) {
	reg := provider.NewRegistry()
	upstream := &countingProvider{err: errors.New("temporary upstream error")}
	reg.Replace(map[string]provider.Provider{"temporary": upstream})
	st := store.NewMemory()
	cfg := testDispatcherConfig()
	cfg.MaxAttempts = 3
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "default", Provider: "temporary", Priority: 1}}, []config.ProviderConfig{{Name: "temporary", Enabled: true}})

	receipt, err := d.Submit(context.Background(), Envelope{
		From: "1069", To: "8613800138000", Text: "hello", RegisteredDelivery: 1,
		Source: SubmitSource{Kind: SourceSMPP, SMPPSessionID: "tx-1", SMPPSystemID: "esme-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "uncertain", 1)
	if count := upstream.callCount(); count != 1 {
		t.Fatalf("provider calls = %d, want 1", count)
	}
	if size := d.PendingSize(); size != 0 {
		t.Fatalf("pending size = %d, want 0", size)
	}
	msg, ok, err := st.GetMessage(context.Background(), receipt.GatewayID)
	if err != nil || !ok || msg.State != "UNKNOWN" {
		t.Fatalf("unexpected uncertain message: ok=%v msg=%+v err=%v", ok, msg, err)
	}
}

func TestDispatcherFlushesSMPPDLRToReceiverBySystemID(t *testing.T) {
	st := store.NewMemory()
	rec := store.Pending{
		ProviderID:         "up-1",
		GatewayID:          "g000000000001",
		SourceKind:         SourceSMPP.String(),
		SourceSession:      "tx-1",
		SourceSystem:       "esme-a",
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Provider:           "mock-a",
		ReceivedAt:         time.Now().UTC(),
		ExpiresAt:          time.Now().Add(time.Hour),
		DLRReady:           true,
		DLRState:           "DELIVRD",
		DLRDoneAt:          time.Now().UTC(),
	}
	if err := st.SavePending(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	receiver := smpp.NewSession(serverConn, smpp.SessionConfig{ID: "rx-1", Auth: func(systemID, password string) bool { return true }})
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), provider.NewRegistry(), fakeSMPPServer{receivers: []*smpp.Session{receiver}}, testDispatcherConfig(), st)
	defer d.Close()
	go receiver.Serve(context.Background())
	if err := smpp.WritePDU(clientConn, smpp.PDU{CommandID: smpp.CommandBindReceiver, SequenceID: 1, Body: bindBodyForDispatchTest("esme-a", "")}); err != nil {
		t.Fatal(err)
	}
	bindResp, err := smpp.ReadPDU(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if bindResp.CommandID != smpp.CommandBindReceiverResp || bindResp.Status != smpp.StatusOK {
		t.Fatalf("unexpected bind response: %+v", bindResp)
	}

	done := make(chan struct{})
	go func() {
		d.FlushDLR("esme-a")
		close(done)
	}()
	pdu, err := smpp.ReadPDU(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if pdu.CommandID != smpp.CommandDeliverSM {
		t.Fatalf("expected deliver_sm, got 0x%08x", pdu.CommandID)
	}
	if err := smpp.WritePDU(clientConn, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, Status: smpp.StatusOK, SequenceID: pdu.SequenceID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("flush did not finish after deliver_sm_resp")
	}
	if _, ok, err := st.GetPending(context.Background(), "mock-a", "up-1"); err != nil || ok {
		t.Fatalf("pending should be deleted after flush: ok=%v err=%v", ok, err)
	}
}

func TestDispatcherPrefersOriginalSessionForSMPPDLR(t *testing.T) {
	st := store.NewMemory()
	rec := store.Pending{
		ProviderID:         "up-1",
		GatewayID:          "g000000000001",
		SourceKind:         SourceSMPP.String(),
		SourceSession:      "trx-original",
		SourceSystem:       "esme-a",
		From:               "1069",
		To:                 "13800138000",
		Text:               "hello",
		RegisteredDelivery: 1,
		Provider:           "mock-a",
		ReceivedAt:         time.Now().UTC(),
		ExpiresAt:          time.Now().Add(time.Hour),
	}
	if err := st.SavePending(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMessage(context.Background(), testMessage(rec.GatewayID)); err != nil {
		t.Fatal(err)
	}

	otherServer, otherClient := net.Pipe()
	defer otherServer.Close()
	defer otherClient.Close()
	origServer, origClient := net.Pipe()
	defer origServer.Close()
	defer origClient.Close()
	other := smpp.NewSession(otherServer, smpp.SessionConfig{ID: "trx-other", Auth: func(systemID, password string) bool { return true }})
	orig := smpp.NewSession(origServer, smpp.SessionConfig{ID: "trx-original", Auth: func(systemID, password string) bool { return true }})
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), provider.NewRegistry(), fakeSMPPServer{
		session:   orig,
		receivers: []*smpp.Session{other, orig},
	}, testDispatcherConfig(), st)
	defer d.Close()
	go other.Serve(context.Background())
	go orig.Serve(context.Background())
	bindSessionForDispatchTest(t, otherClient, smpp.CommandBindTransceiver, smpp.CommandBindTransceiverResp)
	bindSessionForDispatchTest(t, origClient, smpp.CommandBindTransceiver, smpp.CommandBindTransceiverResp)

	done := make(chan error, 1)
	go func() {
		done <- d.HandleDLR(context.Background(), provider.DLR{Provider: "mock-a", ProviderID: "up-1", State: "DELIVRD", DoneAt: time.Now().UTC()})
	}()
	pdu, err := smpp.ReadPDU(origClient)
	if err != nil {
		t.Fatal(err)
	}
	if pdu.CommandID != smpp.CommandDeliverSM {
		t.Fatalf("expected deliver_sm on original session, got 0x%08x", pdu.CommandID)
	}
	if err := smpp.WritePDU(origClient, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, Status: smpp.StatusOK, SequenceID: pdu.SequenceID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("dlr handler did not finish")
	}
}

func bindSessionForDispatchTest(t *testing.T, conn net.Conn, bindCmd, bindResp uint32) {
	t.Helper()
	if err := smpp.WritePDU(conn, smpp.PDU{CommandID: bindCmd, SequenceID: 1, Body: bindBodyForDispatchTest("esme-a", "")}); err != nil {
		t.Fatal(err)
	}
	resp, err := smpp.ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != bindResp || resp.Status != smpp.StatusOK {
		t.Fatalf("unexpected bind response: %+v", resp)
	}
}

func bindBodyForDispatchTest(systemID, password string) []byte {
	body := []byte{}
	body = append(body, smpp.CString(systemID)...)
	body = append(body, smpp.CString(password)...)
	body = append(body, smpp.CString("gateway")...)
	body = append(body, 0x34, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	return body
}

func testMessage(id string) message.Message {
	msg := message.New(id, message.DirectionMT, "1069", "13800138000", "hello")
	msg.State = "sent"
	return msg
}

func TestDispatcherIgnoresDisabledProviderRoutes(t *testing.T) {
	reg := provider.NewRegistry()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig())
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "disabled",
		Prefix:   []string{},
		Provider: "mock-a",
		Priority: 1,
	}}, []config.ProviderConfig{{
		Name:    "mock-a",
		Enabled: false,
	}})

	_, err := d.Submit(context.Background(), Envelope{From: "1069", To: "13800138000", Text: "hello"})
	if err == nil {
		t.Fatal("expected no route error")
	}
}

func testDispatcherConfig() config.DispatcherConfig {
	return config.DispatcherConfig{
		Workers:              1,
		PerWorkerConcurrency: 1,
		ClaimLimit:           10,
		PollIntervalMS:       10,
		PendingTTL:           "1m",
		MaxAttempts:          5,
		ClaimTimeout:         "1m",
	}
}

func waitForPending(t *testing.T, d *Dispatcher, want int) {
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

func waitForOutboxDepth(t *testing.T, st store.Store, state string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if depth, err := st.OutboxDepth(context.Background(), state); err == nil && depth == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	depth, err := st.OutboxDepth(context.Background(), state)
	t.Fatalf("outbox %s depth did not reach %d: depth=%d err=%v", state, want, depth, err)
}

func readPDUForDispatchTest(conn net.Conn, timeout time.Duration) (smpp.PDU, error) {
	type result struct {
		pdu smpp.PDU
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		pdu, err := smpp.ReadPDU(conn)
		resultCh <- result{pdu: pdu, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.pdu, result.err
	case <-time.After(timeout):
		return smpp.PDU{}, errors.New("timed out waiting for pdu")
	}
}
