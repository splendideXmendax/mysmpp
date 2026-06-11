package smpp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestEffectiveEnquirePeriod(t *testing.T) {
	tests := map[string]time.Duration{
		"":      defaultEnquirePeriod,
		"0":     defaultEnquirePeriod,
		"0s":    defaultEnquirePeriod,
		"-5s":   defaultEnquirePeriod,
		"bad":   defaultEnquirePeriod,
		"45s":   45 * time.Second,
		"100ms": 100 * time.Millisecond,
	}
	for raw, want := range tests {
		if got := effectiveEnquirePeriod(raw); got != want {
			t.Fatalf("effectiveEnquirePeriod(%q) = %s, want %s", raw, got, want)
		}
	}
}

func TestServerClosesIdleBoundSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:          "127.0.0.1:0",
		SystemID:      "client-a",
		EnquirePeriod: "80ms",
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

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		pdu, err := ReadPDU(conn)
		if err != nil {
			if err == io.EOF {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if pdu.CommandID == commandEnquireLink {
			continue
		}
	}
	t.Fatal("expected idle bound session to be closed")
}

func TestServerKeepsActiveSessionAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(config.SMPPConfig{
		Addr:          "127.0.0.1:0",
		SystemID:      "client-a",
		EnquirePeriod: "80ms",
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

	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		pdu, err := ReadPDU(conn)
		if err != nil {
			t.Fatalf("active session closed unexpectedly: %v", err)
		}
		if pdu.CommandID == commandEnquireLink {
			if err := WritePDU(conn, PDU{CommandID: commandEnquireLinkResp, SequenceID: pdu.SequenceID}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, ok := server.Session("s1"); !ok {
		t.Fatal("expected active session to remain registered")
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
