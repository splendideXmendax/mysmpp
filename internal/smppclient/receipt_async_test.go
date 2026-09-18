package smppclient

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestSlowReceiptDoesNotBlockSubmitOrHeartbeat(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	cfg := config.DefaultSMPPClientConfig()
	cfg.ResponseTimeoutMS = 500
	c := newConnection(1, Config{SMPP: cfg}, func(DLR) error { close(entered); <-release; return nil })
	c.bound.Store(true)
	out := make(chan smpp.PDU, 4)
	c.setOut(out)
	errs := make(chan error, 1)
	go c.readLoop(a, make(chan struct{}), errs)
	b.SetDeadline(time.Now().Add(2 * time.Second))
	if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandDeliverSM, SequenceID: 42, Body: buildDeliverBodyForTest(4, "id:p1 stat:DELIVRD err:000")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no callback")
	}
	result := make(chan error, 1)
	go func() { _, err := c.submit(context.Background(), []byte("test")); result <- err }()
	var request smpp.PDU
	select {
	case request = <-out:
	case <-time.After(time.Second):
		t.Fatal("no submit")
	}
	if request.CommandID != smpp.CommandSubmitSM {
		t.Fatalf("unexpected command %x", request.CommandID)
	}
	if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandSubmitSMResp, SequenceID: request.SequenceID, Body: smpp.CString("accepted")}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("submit blocked by DLR")
	}
	if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandEnquireLink, SequenceID: 43}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-out:
		if p.CommandID != smpp.CommandEnquireLinkResp {
			t.Fatalf("unexpected early ACK: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat blocked")
	}
	unblock()
	select {
	case p := <-out:
		if p.CommandID != smpp.CommandDeliverSMResp || p.Status != 0 || p.SequenceID != 42 {
			t.Fatalf("bad durable ACK %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no durable ACK")
	}
	b.Close()
	<-errs
}

func TestReceiptQueueFullAndOldConnectionCannotAckNewConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, stopOnce sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	stop := func() { stopOnce.Do(func() { close(done) }) }
	defer unblock()
	defer stop()
	c := newConnection(1, Config{SMPP: config.DefaultSMPPClientConfig()}, func(DLR) error { close(entered); <-release; return nil })
	out := make(chan smpp.PDU, 2)
	c.setOut(out)
	errs, finished := make(chan error, 1), make(chan struct{})
	go func() { c.readLoop(a, done, errs); close(finished) }()
	b.SetDeadline(time.Now().Add(3 * time.Second))
	body := buildDeliverBodyForTest(4, "id:p1 stat:DELIVRD err:000")
	if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandDeliverSM, SequenceID: 1, Body: body}); err != nil {
		t.Fatal(err)
	}
	<-entered
	for i := 2; i <= 130; i++ {
		if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandDeliverSM, SequenceID: uint32(i), Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case p := <-out:
		if p.SequenceID != 130 || p.Status != 8 {
			t.Fatalf("full queue must NACK: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no overload NACK")
	}
	newOut := make(chan smpp.PDU, 4)
	c.setOut(newOut)
	stop()
	b.Close()
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("old workers not joined")
	}
	select {
	case p := <-newOut:
		t.Fatalf("old receipt leaked to new bind: %+v", p)
	default:
	}
}
