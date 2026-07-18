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

func TestBuildSubmitSMPreservesSevenByteUDHInShortMessageLength(t *testing.T) {
	cfg := config.DefaultSMPPClientConfig()
	cfg.LongMessage = "udh"
	cfg.GSM7Packing = "unpacked"
	cfg.SourceTON = 5
	cfg.SourceNPI = 0
	cfg.DestTON = 1
	cfg.DestNPI = 1
	udh := []byte{0x06, 0x08, 0x04, 0x38, 0x74, 0x02, 0x01}
	parts := BuildSubmitSM(Message{
		GatewayID:          "g1",
		SourceAddr:         "MZF7BIT",
		DestAddr:           "8615069232047",
		Text:               repeat("a", 154),
		Encoding:           "gsm7",
		DataCoding:         0x00,
		RegisteredDelivery: 1,
		UDH:                udh,
	}, cfg)
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
	shortMessageLen := len(submit.UDH) + len(submit.Payload)
	if shortMessageLen != 161 {
		t.Fatalf("short_message length=%d, want 161", shortMessageLen)
	}
	if pduLength := 16 + len(parts[0].Body); pduLength != 214 {
		t.Fatalf("pdu length=%d, want 214", pduLength)
	}
}

func TestBuildSubmitSMResplitsOversizedUDHWithoutPayload(t *testing.T) {
	cfg := config.DefaultSMPPClientConfig()
	cfg.LongMessage = "udh"
	udh := []byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x02, 0x01}
	parts := BuildSubmitSM(Message{
		GatewayID:          "g1",
		SourceAddr:         "1069",
		DestAddr:           "13800138000",
		Text:               "这是一个很长的UCS2短信内容，用来验证下游用message_payload携带UDH长短信时，上游仍然按UDH short_message重新分段发送，避免被不支持message_payload的SMSC拒绝。abcdefghijklmnopqrstuvwxyz0123456789",
		Encoding:           "ucs2",
		DataCoding:         0x08,
		RegisteredDelivery: 1,
		UDH:                udh,
	}, cfg)
	if len(parts) < 2 {
		t.Fatalf("expected oversized UDH payload to be split, got %d part(s)", len(parts))
	}
	for i, part := range parts {
		submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: uint32(i + 1), Body: part.Body})
		if err != nil {
			t.Fatal(err)
		}
		if submit.ESMClass&0x40 == 0 {
			t.Fatalf("part %d missing UDHI bit", i+1)
		}
		if submit.UDH == nil || submit.Concat == nil {
			t.Fatalf("part %d missing concat UDH: udh=% x concat=%+v", i+1, submit.UDH, submit.Concat)
		}
		if submit.OptionalParamsOffset < len(part.Body) {
			t.Fatalf("part %d should not contain message_payload TLV", i+1)
		}
		if len(submit.Payload)+len(submit.UDH) > 254 {
			t.Fatalf("part %d short_message too long: %d", i+1, len(submit.Payload)+len(submit.UDH))
		}
	}
}

func TestBuildSubmitSMResplitsDataCodingUCS2EvenWhenDetectedGSM7(t *testing.T) {
	cfg := config.DefaultSMPPClientConfig()
	cfg.LongMessage = "udh"
	parts := BuildSubmitSM(Message{
		GatewayID:          "g1",
		SourceAddr:         "1069",
		DestAddr:           "13800138000",
		Text:               "ASCII text that is intentionally long enough to exceed one UCS2 concatenated segment when data_coding forces UCS2. abcdefghijklmnopqrstuvwxyz 0123456789 abcdefghijklmnopqrstuvwxyz 0123456789",
		DataCoding:         0x08,
		RegisteredDelivery: 1,
		UDH:                []byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x02, 0x01},
	}, cfg)
	if len(parts) < 3 {
		t.Fatalf("expected UCS2-sized split, got %d part(s)", len(parts))
	}
	for i, part := range parts {
		submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: uint32(i + 1), Body: part.Body})
		if err != nil {
			t.Fatal(err)
		}
		if submit.DataCoding != 0x08 {
			t.Fatalf("part %d data_coding=0x%02x", i+1, submit.DataCoding)
		}
		if submit.ESMClass&0x40 == 0 || submit.UDH == nil {
			t.Fatalf("part %d missing UDH: esm_class=0x%02x udh=% x", i+1, submit.ESMClass, submit.UDH)
		}
		if submit.OptionalParamsOffset < len(part.Body) {
			t.Fatalf("part %d should not contain message_payload TLV", i+1)
		}
		if len(submit.Payload)+len(submit.UDH) > 254 {
			t.Fatalf("part %d short_message too long: %d", i+1, len(submit.Payload)+len(submit.UDH))
		}
	}
}

func TestBuildSubmitSMSplitLengthsByDataCoding(t *testing.T) {
	cfg := config.DefaultSMPPClientConfig()
	cfg.LongMessage = "udh"
	cfg.GSM7Packing = "unpacked"

	tests := []struct {
		name       string
		text       string
		dataCoding byte
		wantLen    int
	}{
		{
			name:       "gsm7 unpacked",
			text:       repeat("a", 300),
			dataCoding: 0x00,
			wantLen:    159,
		},
		{
			name:       "8bit",
			text:       repeat("b", 300),
			dataCoding: 0x03,
			wantLen:    140,
		},
		{
			name:       "ucs2",
			text:       repeat("c", 200),
			dataCoding: 0x08,
			wantLen:    140,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := BuildSubmitSM(Message{
				GatewayID:          "g1",
				SourceAddr:         "1069",
				DestAddr:           "13800138000",
				Text:               tt.text,
				DataCoding:         tt.dataCoding,
				RegisteredDelivery: 1,
				UDH:                []byte{0x05, 0x00, 0x03, 0x7b, 0x02, 0x01},
			}, cfg)
			if len(parts) < 2 {
				t.Fatalf("expected split submit, got %d part(s)", len(parts))
			}
			submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 1, Body: parts[0].Body})
			if err != nil {
				t.Fatal(err)
			}
			if submit.DataCoding != tt.dataCoding {
				t.Fatalf("data_coding=0x%02x, want 0x%02x", submit.DataCoding, tt.dataCoding)
			}
			if len(submit.UDH) != 6 {
				t.Fatalf("expected 6-byte UDH, got % x", submit.UDH)
			}
			gotLen := len(submit.UDH) + len(submit.Payload)
			if gotLen != tt.wantLen {
				t.Fatalf("first short_message length=%d, want %d", gotLen, tt.wantLen)
			}
			if submit.OptionalParamsOffset < len(parts[0].Body) {
				t.Fatal("split part should not contain message_payload TLV")
			}
		})
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

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
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
