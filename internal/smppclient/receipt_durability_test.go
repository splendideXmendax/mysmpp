package smppclient

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestReceiptAckWaitsForDurableHandler(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "storage-failure"}[fail], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			entered := make(chan struct{})
			release := make(chan struct{})
			c := newConnection(1, Config{SMPP: config.DefaultSMPPClientConfig()}, func(DLR) error {
				close(entered)
				<-release
				if fail {
					return errors.New("disk unavailable")
				}
				return nil
			})
			out := make(chan smpp.PDU, 2)
			c.setOut(out)
			errs := make(chan error, 1)
			go c.readLoop(a, make(chan struct{}), errs)
			if err := smpp.WritePDU(b, smpp.PDU{CommandID: smpp.CommandDeliverSM, SequenceID: 42, Body: buildDeliverBodyForTest(4, "id:p1 stat:DELIVRD err:000")}); err != nil {
				t.Fatal(err)
			}
			<-entered
			select {
			case <-out:
				t.Fatal("receipt acknowledged before durable handler returned")
			default:
			}
			close(release)
			select {
			case p := <-out:
				if p.SequenceID != 42 || (p.Status != 0) != fail {
					t.Fatalf("ack=%+v", p)
				}
			case <-time.After(time.Second):
				t.Fatal("missing receipt response")
			}
			b.Close()
			<-errs
		})
	}
}

func TestReceiptWithoutDoneDateHasStableIdentity(t *testing.T) {
	d, ok, receipt := ParseDeliverSM(buildDeliverBodyForTest(4, "id:p1 stat:DELIVRD err:000"), "auto", "auto")
	if !ok || !receipt || !d.DoneAt.IsZero() {
		t.Fatalf("timestamp was invented before durable dedup: %+v", d)
	}
}
