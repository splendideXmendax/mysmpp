package provider

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestSMPPProviderSendAndDLR(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	submits := make(chan smpp.SubmitSM, 1)
	upstream := smpp.NewServer(config.SMPPConfig{
		Addr:          "127.0.0.1:0",
		SystemID:      "smsc",
		EnquirePeriod: "30s",
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool {
			return systemID == "acct" && password == "secret88"
		},
		func(session *smpp.Session, submit smpp.SubmitSM) {
			submits <- submit
			providerID := "0000004F"
			session.Send(smpp.PDU{
				CommandID:  smpp.CommandSubmitSMResp,
				Status:     smpp.StatusOK,
				SequenceID: submit.SequenceID,
				Body:       smpp.CString(providerID),
			})
			go func() {
				time.Sleep(50 * time.Millisecond)
				dlr := smpp.BuildDLR(smpp.DLRParams{
					GatewayID:    providerID,
					SourceAddr:   submit.To,
					DestAddr:     submit.From,
					SubmittedAt:  time.Now().UTC(),
					DoneAt:       time.Now().UTC(),
					State:        "DELIVRD",
					ErrorCode:    0,
					OriginalText: submit.Text,
				})
				dlr.SequenceID = session.NextSeq()
				session.Send(dlr)
			}()
			session.CompleteSubmit()
		},
	)
	errCh := make(chan error, 1)
	go func() {
		errCh <- upstream.ListenAndServe(ctx)
	}()
	addr := waitUpstreamAddr(t, upstream)

	smppCfg := config.SMPPClientConfig{
		BindMode:            "transceiver",
		Binds:               1,
		WindowSize:          4,
		EnquirePeriod:       "30s",
		ResponseTimeoutMS:   2000,
		ReconnectMin:        "50ms",
		ReconnectMax:        "100ms",
		SourceTON:           -1,
		SourceNPI:           -1,
		DestTON:             1,
		DestNPI:             1,
		RegisteredDelivery:  -1,
		GSM7Packing:         "unpacked",
		LongMessage:         "udh",
		MessageIDRespFormat: "hex",
		MessageIDDLRFormat:  "hex",
		DLRIDSource:         "auto",
	}
	provider := NewSMPPProvider(ctx, config.ProviderConfig{
		Name:     "smsc-a",
		Protocol: "smpp",
		Endpoint: addr,
		SystemID: "acct",
		Password: "secret88",
		Enabled:  true,
		SMPP:     &smppCfg,
	})
	defer provider.Close()

	dlrs := make(chan DLR, 1)
	provider.OnDLR(func(dlr DLR) {
		dlrs <- dlr
	})
	waitProviderBound(t, provider)

	id, err := provider.Send(OutboundMessage{
		Context:            ctx,
		GatewayID:          "g1",
		SourceAddr:         "10690000",
		DestAddr:           "13800138000",
		Text:               "hello upstream smpp",
		RegisteredDelivery: 1,
		Encoding:           "gsm7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "79" {
		t.Fatalf("expected normalized provider id 79, got %q", id)
	}
	select {
	case submit := <-submits:
		if submit.From != "10690000" || submit.To != "13800138000" || submit.Text != "hello upstream smpp" {
			t.Fatalf("unexpected submit: %+v", submit)
		}
		if submit.RegisteredDelivery != 1 {
			t.Fatalf("expected registered_delivery=1, got %d", submit.RegisteredDelivery)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive submit_sm")
	}
	select {
	case dlr := <-dlrs:
		if dlr.Provider != "smsc-a" || dlr.ProviderID != "79" || dlr.State != "DELIVRD" {
			t.Fatalf("unexpected dlr: %+v", dlr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not receive dlr")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not stop")
	}
}

func waitUpstreamAddr(t *testing.T, server *smpp.Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != "" {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("upstream did not start")
	return ""
}

func waitProviderBound(t *testing.T, provider *SMPPProvider) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := provider.Status()
		for _, conn := range status.Connections {
			if conn.Bound {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider did not bind: %+v", provider.Status())
}
