package smppclient

import (
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestBuildSubmitSMConcatPassthroughUDH(t *testing.T) {
	udh := []byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x03, 0x01}
	parts := BuildSubmitSM(Message{
		GatewayID:          "g1",
		SourceAddr:         "1069",
		DestAddr:           "13800138000",
		Text:               "hello",
		Encoding:           "gsm7",
		RegisteredDelivery: 1,
		UDH:                udh,
	}, config.DefaultSMPPClientConfig())
	if len(parts) != 1 {
		t.Fatalf("expected one passthrough part, got %d", len(parts))
	}

	submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 1, Body: parts[0].Body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.ESMClass&0x40 == 0 {
		t.Fatalf("expected UDHI bit, got esm_class=0x%02x", submit.ESMClass)
	}
	if string(submit.UDH) != string(udh) {
		t.Fatalf("UDH not preserved: got % x want % x", submit.UDH, udh)
	}
	if submit.Concat == nil || submit.Concat.Reference != 0x1234 || submit.Concat.Total != 3 || submit.Concat.Part != 1 {
		t.Fatalf("concat not preserved: %+v", submit.Concat)
	}
	if submit.Text != "hello" {
		t.Fatalf("text not preserved: %q", submit.Text)
	}
}

func TestBuildSubmitSMConcatPassthroughSAR(t *testing.T) {
	cfg := config.DefaultSMPPClientConfig()
	cfg.LongMessage = "sar"
	parts := BuildSubmitSM(Message{
		GatewayID:  "g1",
		SourceAddr: "1069",
		DestAddr:   "13800138000",
		Text:       "hello",
		Encoding:   "gsm7",
		UDH:        []byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x03, 0x02},
	}, cfg)
	if len(parts) != 1 {
		t.Fatalf("expected one passthrough part, got %d", len(parts))
	}

	submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 1, Body: parts[0].Body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.ESMClass&0x40 != 0 {
		t.Fatalf("expected SAR TLV without UDHI, got esm_class=0x%02x", submit.ESMClass)
	}
	if submit.UDH != nil {
		t.Fatalf("expected no UDH when using SAR, got % x", submit.UDH)
	}
	if submit.Concat == nil || submit.Concat.Reference != 0x1234 || submit.Concat.Total != 3 || submit.Concat.Part != 2 {
		t.Fatalf("sar concat not preserved: %+v", submit.Concat)
	}
	if submit.Text != "hello" {
		t.Fatalf("text not preserved: %q", submit.Text)
	}
}

func TestBuildSubmitSMNoUDHUnchanged(t *testing.T) {
	parts := BuildSubmitSM(Message{
		GatewayID:  "g1",
		SourceAddr: "1069",
		DestAddr:   "13800138000",
		Text:       "hello",
		Encoding:   "gsm7",
	}, config.DefaultSMPPClientConfig())
	if len(parts) != 1 {
		t.Fatalf("expected one normal part, got %d", len(parts))
	}

	submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 1, Body: parts[0].Body})
	if err != nil {
		t.Fatal(err)
	}
	if submit.ESMClass&0x40 != 0 || submit.UDH != nil || submit.Concat != nil {
		t.Fatalf("normal submit changed: esm_class=0x%02x udh=% x concat=%+v", submit.ESMClass, submit.UDH, submit.Concat)
	}
	if got := message.DecodeSubmitText(submit.Payload, submit.DataCoding); got != "hello" {
		t.Fatalf("unexpected text: %q", got)
	}
}
