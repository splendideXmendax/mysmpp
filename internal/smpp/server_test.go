package smpp

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
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

func TestParseSubmitSMStripsUDH(t *testing.T) {
	body := submitSMBodyWith(0x40, 0x00, append([]byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x02, 0x01}, message.EncodeText("hello", 0x00)...))
	submit, err := ParseSubmitSM(PDU{CommandID: commandSubmitSM, SequenceID: 8, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.Text != "hello" {
		t.Fatalf("expected UDH stripped text, got %q", submit.Text)
	}
	if submit.Concat == nil || submit.Concat.Reference != 0x1234 || submit.Concat.Total != 2 || submit.Concat.Part != 1 {
		t.Fatalf("concat not parsed: %+v", submit.Concat)
	}
}

func TestParseSubmitSMMessagePayloadTLV(t *testing.T) {
	body := submitSMBodyWith(0x00, 0x00, nil)
	body = appendTLV(body, TagMessagePayload, message.EncodeText("payload text", 0x00))
	submit, err := ParseSubmitSM(PDU{CommandID: commandSubmitSM, SequenceID: 9, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.Text != "payload text" {
		t.Fatalf("expected message_payload text, got %q", submit.Text)
	}
}

func TestParseSubmitSMSARTLV(t *testing.T) {
	body := submitSMBodyWith(0x00, 0x00, message.EncodeText("part", 0x00))
	body = appendTLV(body, TagSARMsgRefNum, []byte{0x12, 0x34})
	body = appendTLV(body, TagSARTotalSegments, []byte{0x02})
	body = appendTLV(body, TagSARSegmentSeqnum, []byte{0x02})
	submit, err := ParseSubmitSM(PDU{CommandID: commandSubmitSM, SequenceID: 10, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.Concat == nil || submit.Concat.Reference != 0x1234 || submit.Concat.Total != 2 || submit.Concat.Part != 2 {
		t.Fatalf("sar tlv not parsed: %+v", submit.Concat)
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

func TestServerRejectsWhenMaxSessionsReached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:        "127.0.0.1:0",
		SystemID:    "client-a",
		MaxSessions: 1,
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool { return true },
		nil,
	)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	addr := waitServerAddr(t, server)
	conn1, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	conn2, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	_ = conn2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := ReadPDU(conn2); err == nil {
		t.Fatal("expected second connection to be closed")
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

func TestServerRejectsOverlongBindPassword(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "client-a",
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool { return true },
		nil,
	)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	conn, err := net.DialTimeout("tcp", waitServerAddr(t, server), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WritePDU(conn, PDU{CommandID: commandBindTransceiver, SequenceID: 1, Body: bindBody("client-a", "password-too-long")}); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != statusInvalidPassword {
		t.Fatalf("expected invalid password status, got %+v", resp)
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

func TestServerRejectsWhenMaxSessionsPerSystemIDReached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:                   "127.0.0.1:0",
		SystemID:               "client-a",
		MaxSessions:            4,
		MaxSessionsPerSystemID: 1,
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool { return true },
		nil,
	)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	addr := waitServerAddr(t, server)
	conn1, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	if err := WritePDU(conn1, PDU{CommandID: commandBindTransceiver, SequenceID: 1, Body: bindBody("client-a", "secret")}); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadPDU(conn1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != statusOK {
		t.Fatalf("expected first bind ok, got %+v", resp)
	}

	conn2, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if err := WritePDU(conn2, PDU{CommandID: commandBindTransceiver, SequenceID: 1, Body: bindBody("client-a", "secret")}); err != nil {
		t.Fatal(err)
	}
	resp, err = ReadPDU(conn2)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != statusBindFailed {
		t.Fatalf("expected second bind rejected, got %+v", resp)
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

func TestServerReceiversBySystemID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:        "127.0.0.1:0",
		SystemID:    "client-a",
		MaxSessions: 3,
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool { return true },
		nil,
	)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()
	addr := waitServerAddr(t, server)

	tx := bindTestClient(t, addr, commandBindTransmitter)
	defer tx.Close()
	rx := bindTestClient(t, addr, commandBindReceiver)
	defer rx.Close()
	trx := bindTestClient(t, addr, commandBindTransceiver)
	defer trx.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if receivers := server.ReceiversBySystemID("client-a"); len(receivers) == 2 {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not stop")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected two receiving sessions, got %d", len(server.ReceiversBySystemID("client-a")))
}

func TestServerSendsEnquireLink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:          "127.0.0.1:0",
		SystemID:      "client-a",
		EnquirePeriod: "50ms",
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func(systemID, password string) bool { return true },
		nil,
	)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()

	conn, err := net.DialTimeout("tcp", waitServerAddr(t, server), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WritePDU(conn, PDU{CommandID: commandBindTransceiver, SequenceID: 1, Body: bindBody("client-a", "secret")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPDU(conn); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	pdu, err := ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if pdu.CommandID != commandEnquireLink {
		t.Fatalf("expected enquire_link, got %+v", pdu)
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

func bindTestClient(t *testing.T, addr string, commandID uint32) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePDU(conn, PDU{CommandID: commandID, SequenceID: 1, Body: bindBody("client-a", "secret")}); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadPDU(conn)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != statusOK {
		t.Fatalf("unexpected bind response: %+v", resp)
	}
	return conn
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
	return submitSMBodyWithAddr(from, to, 0x00, 0x00, message.EncodeText(text, 0x00))
}

func submitSMBodyWith(esmClass, dataCoding byte, shortMessage []byte) []byte {
	return submitSMBodyWithAddr("1069", "13800138000", esmClass, dataCoding, shortMessage)
}

func submitSMBodyWithAddr(from, to string, esmClass, dataCoding byte, shortMessage []byte) []byte {
	body := []byte{}
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString(from)...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString(to)...)
	body = append(body, esmClass, 0x00, 0x00)
	body = append(body, CString("")...)
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x00, dataCoding, 0x00)
	body = append(body, byte(len(shortMessage)))
	body = append(body, shortMessage...)
	return body
}

func appendTLV(body []byte, tag uint16, value []byte) []byte {
	var header [4]byte
	binary.BigEndian.PutUint16(header[0:2], tag)
	binary.BigEndian.PutUint16(header[2:4], uint16(len(value)))
	body = append(body, header[:]...)
	return append(body, value...)
}
