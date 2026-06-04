package smpp

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestParseSubmitSM(t *testing.T) {
	msg, err := parseSubmitSM(PDU{CommandID: commandSubmitSM, SequenceID: 7, Body: submitSMBody("1069", "13800138000", "hello")})
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "1069" || msg.To != "13800138000" || msg.Text != "hello" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestSMPPClientServerSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	submits := make(chan SubmitSM, 1)
	server := NewServer(config.SMPPConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "client-a",
		Password: "secret",
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool {
			return systemID == "client-a" && password == "secret"
		},
		func(session *Session, submit SubmitSM) {
			submits <- submit
			session.Send(PDU{CommandID: commandSubmitSMResp, Status: statusOK, SequenceID: submit.SequenceID, Body: CString("g0000000001")})
		},
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx)
	}()

	addr := waitServerAddr(t, server)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := WritePDU(conn, PDU{CommandID: commandBindTransceiver, SequenceID: 1, Body: bindBody("client-a", "secret")}); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != commandBindTransceiverResp || resp.Status != statusOK {
		t.Fatalf("unexpected bind response: %+v", resp)
	}

	if err := WritePDU(conn, PDU{CommandID: commandSubmitSM, SequenceID: 2, Body: submitSMBody("1069", "13800138000", "hello smpp")}); err != nil {
		t.Fatal(err)
	}
	resp, err = ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != commandSubmitSMResp || resp.Status != statusOK {
		t.Fatalf("unexpected submit response: %+v", resp)
	}

	select {
	case submit := <-submits:
		if submit.From != "1069" || submit.To != "13800138000" || submit.Text != "hello smpp" {
			t.Fatalf("unexpected submit: %+v", submit)
		}
		if submit.RegisteredDelivery != 0x01 {
			t.Fatalf("expected registered delivery, got 0x%02x", submit.RegisteredDelivery)
		}
	case <-time.After(time.Second):
		t.Fatal("submit handler not called")
	}

	if err := WritePDU(conn, PDU{CommandID: commandEnquireLink, SequenceID: 3}); err != nil {
		t.Fatal(err)
	}
	resp, err = ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != commandEnquireLinkResp || resp.SequenceID != 3 {
		t.Fatalf("unexpected enquire_link response: %+v", resp)
	}

	if err := WritePDU(conn, PDU{CommandID: commandUnbind, SequenceID: 4}); err != nil {
		t.Fatal(err)
	}
	resp, err = ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.CommandID != commandUnbindResp || resp.SequenceID != 4 {
		t.Fatalf("unexpected unbind response: %+v", resp)
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

func waitServerAddr(t *testing.T, server *Server) string {
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
	body = append(body, CString(systemID)...)
	body = append(body, CString(password)...)
	body = append(body, CString("gateway")...)
	body = append(body, 0x34, 0x00, 0x00)
	body = append(body, CString("")...)
	return body
}

func submitSMBody(from, to, text string) []byte {
	body := []byte{}
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString(from)...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString(to)...)
	body = append(body, 0x00, 0x00, 0x00)
	body = append(body, CString("")...)
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x00, 0x00, 0x00)
	body = append(body, byte(len(text)))
	body = append(body, []byte(text)...)
	return body
}
