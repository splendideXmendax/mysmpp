package core

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestRelayPushesDLRToSubmittingSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	relay := New(logger)
	mock := provider.NewMock()
	mock.DelayMin = 10 * time.Millisecond
	mock.DelayMax = 10 * time.Millisecond
	relay.SetProvider(mock)
	server := smpp.NewServer(config.SMPPConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "mysmpp",
	}, logger, func(systemID, password string) bool {
		return systemID == "esme1" && password == "secret"
	}, relay.OnSubmit)
	relay.SetServer(server)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()

	conn, err := net.DialTimeout("tcp", waitServerAddr(t, server), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandBindTransceiver, SequenceID: 1, Body: bindBody("esme1", "secret")}); err != nil {
		t.Fatal(err)
	}
	resp, err := smpp.ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != smpp.CommandBindTransceiverResp || resp.Status != smpp.StatusOK {
		t.Fatalf("unexpected bind resp: %+v", resp)
	}

	if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 2, Body: submitSMBody("1069", "13800138000", "hello dlr")}); err != nil {
		t.Fatal(err)
	}
	resp, err = smpp.ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != smpp.CommandSubmitSMResp || resp.Status != smpp.StatusOK {
		t.Fatalf("unexpected submit resp: %+v", resp)
	}
	offset := 0
	gatewayID := readCString(resp.Body, &offset)
	if gatewayID != "g0000000001" {
		t.Fatalf("unexpected gateway id %q", gatewayID)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	dlrPDU, err := smpp.ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if dlrPDU.CommandID != smpp.CommandDeliverSM {
		t.Fatalf("expected deliver_sm, got %+v", dlrPDU)
	}
	dlr := parseDeliverSM(dlrPDU.Body)
	if dlr.from != "13800138000" || dlr.to != "1069" {
		t.Fatalf("dlr direction not reversed: %+v", dlr)
	}
	if !strings.Contains(dlr.text, "id:g0000000001") || !strings.Contains(dlr.text, "stat:DELIVRD") || !strings.Contains(dlr.text, "text:hello dlr") {
		t.Fatalf("unexpected dlr text %q", dlr.text)
	}
	if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, SequenceID: dlrPDU.SequenceID}); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func waitServerAddr(t *testing.T, server *smpp.Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != "" {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return ""
}

func bindBody(systemID, password string) []byte {
	body := []byte{}
	body = append(body, smpp.CString(systemID)...)
	body = append(body, smpp.CString(password)...)
	body = append(body, smpp.CString("gateway")...)
	body = append(body, 0x34, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	return body
}

func submitSMBody(from, to, text string) []byte {
	body := []byte{}
	body = append(body, smpp.CString("")...)
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString(from)...)
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString(to)...)
	body = append(body, 0x00, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	body = append(body, smpp.CString("")...)
	body = append(body, 0x01, 0x00, 0x00, 0x00)
	body = append(body, byte(len(text)))
	body = append(body, []byte(text)...)
	return body
}

type deliverSM struct {
	from string
	to   string
	text string
}

func parseDeliverSM(body []byte) deliverSM {
	offset := 0
	_ = readCString(body, &offset)
	offset += 2
	from := readCString(body, &offset)
	offset += 2
	to := readCString(body, &offset)
	offset += 3
	_ = readCString(body, &offset)
	_ = readCString(body, &offset)
	offset += 4
	smLen := int(body[offset])
	offset++
	return deliverSM{from: from, to: to, text: string(body[offset : offset+smLen])}
}

func readCString(body []byte, offset *int) string {
	start := *offset
	for *offset < len(body) && body[*offset] != 0x00 {
		(*offset)++
	}
	value := string(body[start:*offset])
	if *offset < len(body) {
		(*offset)++
	}
	return value
}
