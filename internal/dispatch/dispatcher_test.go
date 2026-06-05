package dispatch

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type fakeSMPPServer struct {
	session *smpp.Session
}

func (s fakeSMPPServer) Session(id string) (*smpp.Session, bool) {
	if s.session != nil && s.session.ID() == id {
		return s.session, true
	}
	return nil, false
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
	if receipt.GatewayID != "g0000000001" || receipt.Provider != "mock-a" || receipt.Route != "mobile" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	waitForPending(t, d, 1)
	if d.PendingSize() != 1 {
		t.Fatalf("expected one pending record, got %d", d.PendingSize())
	}
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
