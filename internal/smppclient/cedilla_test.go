package smppclient

import (
	"bytes"
	"strings"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestCedillaSubmitWireEncoding(t *testing.T) {
	for _, packing := range []string{"packed", "unpacked"} {
		for _, mode := range []string{"udh", "sar", "payload"} {
			t.Run(packing+"/"+mode, func(t *testing.T) {
				cfg := config.DefaultSMPPClientConfig()
				cfg.GSM7Packing, cfg.LongMessage = packing, mode
				for _, tc := range []struct {
					name string
					msg  Message
					dcs  byte
					wire []byte
				}{
					{"auto", Message{Text: "ç"}, 0, []byte{0x09}},
					{"auto-name", Message{Text: "ç", Encoding: "auto"}, 0, []byte{0x09}},
					{"capital", Message{Text: "Ç"}, 0, []byte{0x09}},
					{"explicit-dcs8", Message{Text: "ç", DataCoding: 8}, 8, []byte{0, 0xe7}},
					{"explicit-ucs2", Message{Text: "ç", Encoding: "ucs2"}, 8, []byte{0, 0xe7}},
					{"raw-dcs0", Message{Text: "ç", RawPayloadSet: true, RawPayload: []byte{0x09}}, 0, []byte{0x09}},
					{"raw-dcs3", Message{Text: "ç", DataCoding: 3, RawPayloadSet: true, RawPayload: []byte{0xe7}}, 3, []byte{0xe7}},
					{"raw-dcs8", Message{Text: "ç", DataCoding: 8, RawPayloadSet: true, RawPayload: []byte{0, 0xe7}}, 8, []byte{0, 0xe7}},
				} {
					t.Run(tc.name, func(t *testing.T) {
						tc.msg.SourceAddr, tc.msg.DestAddr = "Brand", "8613800138000"
						parts := BuildSubmitSM(tc.msg, cfg)
						if len(parts) != 1 {
							t.Fatalf("got %d parts, want 1", len(parts))
						}
						submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, Body: parts[0].Body})
						if err != nil {
							t.Fatal(err)
						}
						// Check wire bytes, not the receiver's ambiguous 0x09 display.
						if submit.DataCoding != tc.dcs || !bytes.Equal(submit.Payload, tc.wire) {
							t.Fatalf("DCS=%d payload=%x, want DCS=%d payload=%x", submit.DataCoding, submit.Payload, tc.dcs, tc.wire)
						}
					})
				}
				parts := BuildSubmitSM(Message{Text: strings.Repeat("ç", 161), SourceAddr: "Brand", DestAddr: "8613800138000"}, cfg)
				wantParts := 2
				if mode == "payload" {
					wantParts = 1
				}
				if len(parts) != wantParts {
					t.Fatalf("long message parts=%d want=%d", len(parts), wantParts)
				}
				for i, part := range parts {
					submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, Body: part.Body})
					if err != nil {
						t.Fatal(err)
					}
					if submit.DataCoding != 0 {
						t.Fatalf("long message DCS=%d, want 0", submit.DataCoding)
					}
					if mode != "payload" && (submit.Concat == nil || submit.Concat.Total != 2 || int(submit.Concat.Part) != i+1) {
						t.Fatalf("concat metadata changed: %+v", submit.Concat)
					}
					if packing == "unpacked" {
						n := 153
						if i == 1 {
							n = 8
						} else if mode == "payload" {
							n = 161
						}
						if !bytes.Equal(submit.Payload, bytes.Repeat([]byte{0x09}, n)) {
							t.Fatalf("long message lost cedilla bytes: %x", submit.Payload)
						}
					}
				}
			})
		}
	}
}
