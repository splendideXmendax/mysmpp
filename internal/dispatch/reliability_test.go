package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func waitReceiptState(t *testing.T, st store.Store, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		counts, err := st.ReceiptCounts(context.Background())
		if err == nil && counts[key] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	counts, err := st.ReceiptCounts(context.Background())
	t.Fatalf("receipt %s want %d: %v %v", key, want, counts, err)
}

func TestMessageLengthBoundaries(t *testing.T) {
	d := &Dispatcher{}
	d.ReloadLimits(config.DispatcherConfig{})
	for _, tc := range []struct {
		name string
		env  Envelope
		want int
		bad  bool
	}{
		{"gsm20", Envelope{Text: strings.Repeat("a", 153*20)}, 20, false},
		{"gsm21", Envelope{Text: strings.Repeat("a", 153*20+1)}, 0, true},
		{"ucs20", Envelope{Text: strings.Repeat("界", 67*20)}, 20, false},
		{"ucs21", Envelope{Text: strings.Repeat("界", 67*20+1)}, 0, true},
		{"udh20", Envelope{UDH: []byte{5, 0, 3, 7, 20, 1}, RawPayloadSet: true, RawPayload: bytes.Repeat([]byte{'a'}, 153)}, 1, false},
		{"udh21", Envelope{UDH: []byte{5, 0, 3, 7, 21, 1}, RawPayloadSet: true, RawPayload: []byte{'a'}}, 0, true},
		{"udh16-bound", Envelope{UDH: []byte{6, 8, 4, 0, 7, 2, 1}, RawPayloadSet: true, RawPayload: bytes.Repeat([]byte{'a'}, 152)}, 1, false},
		{"udh16-over", Envelope{UDH: []byte{6, 8, 4, 0, 7, 2, 1}, RawPayloadSet: true, RawPayload: bytes.Repeat([]byte{'a'}, 153)}, 0, true},
		{"binary20", Envelope{DataCoding: 4, RawPayloadSet: true, RawPayload: make([]byte, 134*20)}, 20, false},
		{"binary21", Envelope{DataCoding: 4, RawPayloadSet: true, RawPayload: make([]byte, 134*20+1)}, 0, true},
		{"odd-ucs", Envelope{DataCoding: 8, RawPayloadSet: true, RawPayload: make([]byte, 3)}, 0, true},
		{"sar20", Envelope{SARSet: true, SARRefNum: []byte{0, 8}, SARTotalSegments: []byte{20}, SARSegmentSeqnum: []byte{2}, RawPayloadSet: true, RawPayload: []byte{'a'}}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := d.validateMessageLength(tc.env)
			if errors.Is(err, ErrInvalidMessageLength) != tc.bad || (!tc.bad && len(parts) != tc.want) {
				t.Fatalf("parts=%d err=%v", len(parts), err)
			}
		})
	}
}

func TestMultipartPinSurvivesConfigChangeAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"a": captureProvider{}, "b": captureProvider{}})
	cfg := testDispatcherConfig()
	cfg.PollIntervalMS = 60000
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	d.ReloadRoutes([]config.RouteConfig{{Name: "r", Provider: "a"}}, []config.ProviderConfig{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}})
	env := Envelope{From: "brand", To: "+8613800138000", ClientID: "smpp:customer", Text: "part1", UDH: []byte{5, 0, 3, 99, 3, 1}, RawPayloadSet: true, RawPayload: []byte("part1"), Source: SubmitSource{Kind: SourceSMPP, SMPPSystemID: "customer"}}
	first, err := d.Submit(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	st, err = store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d = New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "r", Provider: "b"}}, []config.ProviderConfig{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}})
	env.UDH[5] = 2
	env.Text = "part2"
	env.RawPayload = []byte(env.Text)
	second, err := d.Submit(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != "a" || second.Provider != first.Provider || first.GatewayID == second.GatewayID {
		t.Fatalf("unexpected pin: %+v %+v", first, second)
	}
	env.Text = "conflicting"
	env.RawPayload = []byte(env.Text)
	if _, err = d.Submit(context.Background(), env); !errors.Is(err, store.ErrMultipartConflict) {
		t.Fatalf("reference collision: %v", err)
	}
	env.ClientID = "smpp:other"
	env.Source.SMPPSystemID = "other"
	other, err := d.Submit(context.Background(), env)
	if err != nil || other.Provider != "b" {
		t.Fatalf("account isolation %+v %v", other, err)
	}
}

func TestReceiptInboxSurvivesRestartBeforeMappingAndCallbackRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	bodies := make(chan []byte, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload json.RawMessage
		json.NewDecoder(r.Body).Decode(&payload)
		bodies <- payload
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	d := &Dispatcher{store: st, pendingTTL: time.Hour}
	e := provider.DLR{Provider: "p", ProviderID: "up", State: "DELIVRD"}
	if err = d.OnDLR(e); err != nil {
		t.Fatal(err)
	}
	if err = d.OnDLR(e); err != nil {
		t.Fatal(err)
	} // omitted done_date deduplicates
	st, err = store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveMessage(context.Background(), testMessage("g")); err != nil {
		t.Fatal(err)
	}
	if err = st.SavePending(context.Background(), store.Pending{Provider: "p", ProviderID: "up", GatewayID: "g", SourceKind: "http", CallbackURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	d = New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), provider.NewRegistry(), nil, testDispatcherConfig(), st)
	d.setHTTPClient(srv.Client())
	defer d.Close()
	waitReceiptState(t, st, "inbox:done", 1)
	waitReceiptState(t, st, "delivery:done", 1)
	first, second := <-bodies, <-bodies
	if !bytes.Equal(first, second) {
		t.Fatalf("retry changed immutable callback: %s / %s", first, second)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCallbackHTTPAndHTTPSCompatibility(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[secure], func(t *testing.T) {
			bodies := make(chan map[string]any, 1)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Error("callback contract changed")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies <- body
				w.WriteHeader(http.StatusNoContent)
			})
			srv := httptest.NewUnstartedServer(handler)
			if secure {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()
			// Trusted test transport permits only this local fixture; production
			// continues to use the public-address dialer tested below.
			d := &Dispatcher{httpClient: srv.Client()}
			rec := store.Pending{CallbackURL: srv.URL, GatewayID: "gateway", ClientMsgID: "order", SegmentIndex: 1, SegmentCount: 2}
			e := provider.DLR{Provider: "p", ProviderID: "up", State: "DELIVRD", DoneAt: time.Now().UTC()}
			if err := d.sendHTTPCallback(context.Background(), rec, e, dlrAggregate{State: "PENDING"}); err != nil {
				t.Fatal(err)
			}
			body := <-bodies
			deliveryID, _ := body["delivery_id"].(string)
			if body["client_msg_id"] != "order" || body["gateway_id"] != "gateway" || body["segment_count"] != float64(2) || body["final"] != false || deliveryID == "" {
				t.Fatalf("callback fields changed: %v", body)
			}
		})
	}
}

func TestCallbackRedirectAndAddressPolicy(t *testing.T) {
	var leaked atomic.Int32
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer httpTarget.Close()
	tlsSource := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, httpTarget.URL, 302) }))
	defer tlsSource.Close()
	httpSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, httpTarget.URL, 307) }))
	defer httpSource.Close()
	d := &Dispatcher{httpClient: tlsSource.Client()}
	for _, url := range []string{httpSource.URL, tlsSource.URL, "https://user:pass@example.com/cb", "http://user:pass@example.com/cb", "ftp://example.com/cb"} {
		err := d.sendHTTPCallback(context.Background(), store.Pending{CallbackURL: url}, provider.DLR{}, dlrAggregate{})
		if !errors.Is(err, errUnsafeCallback) {
			t.Fatalf("URL policy err=%v", err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("callback followed a redirect")
	}
	// Use the real production transport: both schemes must reject loopback
	// before opening a connection, regardless of URL admission.
	d.setHTTPClient(newCallbackClient())
	for _, url := range []string{httpTarget.URL, tlsSource.URL} {
		if err := d.sendHTTPCallback(context.Background(), store.Pending{CallbackURL: url}, provider.DLR{}, dlrAggregate{}); !errors.Is(err, errUnsafeCallback) {
			t.Fatalf("private callback address allowed: %v", err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("callback reached a private address")
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "100.64.1.1", "fc00::1"} {
		if publicCallbackIP(netip.MustParseAddr(ip)) {
			t.Fatalf("private address allowed: %s", ip)
		}
	}
	if !publicCallbackIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public address blocked")
	}
}

func TestSMPPDLRRejectsReusedSessionFromAnotherAccount(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	session := smpp.NewSession(a, smpp.SessionConfig{ID: "s1", Auth: func(string, string) bool { return true }})
	go session.Serve(context.Background())
	bindSessionForDispatchTest(t, b, smpp.CommandBindTransceiver, smpp.CommandBindTransceiverResp) // esme-a
	d := &Dispatcher{smppSrv: fakeSMPPServer{session: session}, logger: slog.Default()}
	for _, account := range []string{"esme-b", ""} {
		if err := d.pushSMPPDLR(store.Pending{SourceSession: "s1", SourceSystem: account}, provider.DLR{}); !errors.Is(err, errNoReceiverOnline) {
			t.Fatalf("cross-account receipt err=%v", err)
		}
	}
}

type partialProvider struct{ calls atomic.Int32 }

func (p *partialProvider) OnDLR(provider.DLRCallback) {}
func (p *partialProvider) Send(provider.OutboundMessage) (string, error) {
	return "", errors.New("use SendAll")
}
func (p *partialProvider) SendAll(provider.OutboundMessage) ([]string, error) {
	p.calls.Add(1)
	return []string{"accepted-1"}, smppclient.PartialSubmitError{Err: smppclient.PermanentError{Err: smppclient.SubmitStatusError{Status: 0x58}}, Total: 3}
}

func TestPartialSendPreservesAcceptedMappingAndNeverResends(t *testing.T) {
	p := &partialProvider{}
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"p": p})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "r", Provider: "p"}}, []config.ProviderConfig{{Name: "p", Enabled: true}})
	r, err := d.Submit(context.Background(), Envelope{From: "brand", To: "+8613800138000", Text: strings.Repeat("x", 400), Source: SubmitSource{Kind: SourceHTTPAPI}})
	if err != nil {
		t.Fatal(err)
	}
	waitForOutboxDepth(t, st, "failed", 1)
	rows, err := st.ListPendingByGatewayID(context.Background(), r.GatewayID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err = d.OnDLR(provider.DLR{Provider: "p", ProviderID: "accepted-1", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	waitReceiptState(t, st, "inbox:done", 1)
	m, ok, err := st.GetMessage(context.Background(), r.GatewayID)
	if err != nil || !ok || m.State != "REJECTD" {
		t.Fatalf("aggregate=%+v %v", m, err)
	}
	if n, err := st.RequeueStaleOutbox(context.Background(), time.Now().Add(time.Hour), 100); n != 0 || err != nil {
		t.Fatalf("requeue=%d %v", n, err)
	}
	if p.calls.Load() != 1 {
		t.Fatal(fmt.Sprintf("sent %d times", p.calls.Load()))
	}
}

func TestQuotaExhaustionDoesNotBlockAlreadyAcceptedSMPPReceipt(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	receiver := smpp.NewSession(a, smpp.SessionConfig{ID: "quota-session", Auth: func(string, string) bool { return true }})
	go receiver.Serve(context.Background())
	bindSessionForDispatchTest(t, b, smpp.CommandBindTransceiver, smpp.CommandBindTransceiverResp)
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"p": multiIDProvider{ids: []string{"quota-up"}}})
	st := store.NewMemory()
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, fakeSMPPServer{session: receiver, receivers: []*smpp.Session{receiver}}, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{Name: "r", Provider: "p"}}, []config.ProviderConfig{{Name: "p", Enabled: true}})
	d.ReloadTenants(config.Config{Tenants: []config.TenantConfig{{TenantID: "customer", Limits: config.TenantLimits{DailySegments: 1, Timezone: "UTC"}}}, ESMEs: []config.ESMECred{{SystemID: "esme-a", TenantID: "customer"}}})
	env := Envelope{From: "brand", To: "8613800138000", Text: "ok", RegisteredDelivery: 1, ClientID: "smpp:esme-a", ClientMsgID: "one", Source: SubmitSource{Kind: SourceSMPP, SMPPSystemID: "esme-a", SMPPSessionID: receiver.ID()}}
	first, err := d.Submit(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	env.ClientMsgID = "two"
	if _, err = d.Submit(context.Background(), env); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota rejection=%v", err)
	}
	waitForPending(t, d, 1)
	if err = d.OnDLR(provider.DLR{Provider: "p", ProviderID: "quota-up", State: "DELIVRD"}); err != nil {
		t.Fatal(err)
	}
	pdu, err := readPDUForDispatchTest(b, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pdu.CommandID != smpp.CommandDeliverSM || !bytes.Contains(pdu.Body, []byte("id:"+first.GatewayID)) || !bytes.Contains(pdu.Body, []byte("stat:DELIVRD")) {
		t.Fatalf("missing accepted receipt after quota rejection: %+v", pdu)
	}
	if err = smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, SequenceID: pdu.SequenceID}); err != nil {
		t.Fatal(err)
	}
	waitReceiptState(t, st, "delivery:done", 1)
}
