package dispatch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type fakeSMPPServer struct {
	session   *smpp.Session
	receivers []*smpp.Session
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
	if receipt.GatewayID != "g000000000001" || receipt.Provider != "mock-a" || receipt.Route != "mobile" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	waitForPending(t, d, 1)
	if d.PendingSize() != 1 {
		t.Fatalf("expected one pending record, got %d", d.PendingSize())
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
	mock.DelayMin = time.Hour
	mock.DelayMax = time.Hour
	reg.Replace(map[string]provider.Provider{"mock-a": mock})
	defer reg.CloseAll()

	st := store.NewMemory()
	cfg := testDispatcherConfig()
	cfg.Workers = 1
	cfg.PerWorkerConcurrency = 1
	d := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reg, nil, cfg, st)
	defer d.Close()
	d.ReloadRoutes([]config.RouteConfig{{
		Name:     "default",
		Prefix:   []string{},
		Provider: "mock-a",
		Priority: 1,
	}}, []config.ProviderConfig{{Name: "mock-a", Enabled: true}})

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
	var first string
	for receipt := range receipts {
		if first == "" {
			first = receipt.GatewayID
			continue
		}
		if receipt.GatewayID != first {
			t.Fatalf("expected same gateway id for duplicate submits, got %q and %q", first, receipt.GatewayID)
		}
	}
	if depth, err := st.OutboxDepth(context.Background(), ""); err != nil {
		t.Fatal(err)
	} else if depth != 1 {
		t.Fatalf("expected one outbox row, got %d", depth)
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
	pending, ok, err := st.GetPending(context.Background(), "up-1")
	if err != nil || !ok {
		t.Fatalf("pending not kept: ok=%v err=%v", ok, err)
	}
	if !pending.DLRReady || pending.DLRState != "DELIVRD" {
		t.Fatalf("dlr not marked ready: %+v", pending)
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

	d.FlushDLR("esme-a")

	pdu, err := smpp.ReadPDU(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if pdu.CommandID != smpp.CommandDeliverSM {
		t.Fatalf("expected deliver_sm, got 0x%08x", pdu.CommandID)
	}
	if _, ok, err := st.GetPending(context.Background(), "up-1"); err != nil || ok {
		t.Fatalf("pending should be deleted after flush: ok=%v err=%v", ok, err)
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
