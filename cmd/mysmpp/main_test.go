package main

import (
	"errors"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

func TestSMPPSubmitQuotaErrorsMapToThrottled(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want uint32
	}{
		{err: dispatch.ErrRateExceeded, want: smpp.StatusThrottled},
		{err: dispatch.ErrQuotaExceeded, want: smpp.StatusThrottled},
		{err: errors.New("other"), want: smpp.StatusSubmitFailed},
	} {
		got := smppSubmitErrorStatus(tc.err)
		want := tc.want
		if got != want {
			t.Fatalf("status for %v = 0x%08x, want 0x%08x", tc.err, got, want)
		}
	}
}

func TestSMPPClientMsgIDIncludesRawPayload(t *testing.T) {
	base := smpp.SubmitSM{
		SequenceID: 2,
		From:       "1069",
		To:         "8613800138000",
		Text:       "",
		DataCoding: 0,
	}
	first := base
	first.Payload = []byte{0x1b, 0x65}
	second := base
	second.Payload = []byte{0x1b, 0x40}
	if smppClientMsgID("session-1", "esme", first) == smppClientMsgID("session-1", "esme", second) {
		t.Fatal("different raw payloads shared an idempotency key")
	}
	if smppClientMsgID("session-1", "esme", first) != smppClientMsgID("session-1", "esme", first) {
		t.Fatal("identical SMPP retries did not share an idempotency key")
	}
	if smppClientMsgID("session-1", "esme", first) == smppClientMsgID("session-2", "esme", first) {
		t.Fatal("different SMPP sessions shared an idempotency key")
	}
}

func TestSMPPClientMsgIDIncludesSAR(t *testing.T) {
	base := smpp.SubmitSM{SequenceID: 2, From: "1069", To: "8613800138000", DataCoding: 0, Payload: []byte("part")}
	first := base
	first.TLVs = []smpp.TLV{
		{Tag: smpp.TagSARMsgRefNum, Value: []byte{0x12, 0x34}},
		{Tag: smpp.TagSARTotalSegments, Value: []byte{0x02}},
		{Tag: smpp.TagSARSegmentSeqnum, Value: []byte{0x01}},
	}
	second := base
	second.TLVs = []smpp.TLV{
		{Tag: smpp.TagSARMsgRefNum, Value: []byte{0x12, 0x35}},
		{Tag: smpp.TagSARTotalSegments, Value: []byte{0x02}},
		{Tag: smpp.TagSARSegmentSeqnum, Value: []byte{0x01}},
	}
	if smppClientMsgID("session-1", "esme", first) == smppClientMsgID("session-1", "esme", second) {
		t.Fatal("different SAR metadata shared an idempotency key")
	}
}

func TestSMPPSubmitEnvelopePreservesSAR(t *testing.T) {
	submit := smpp.SubmitSM{
		From: "1069", To: "8613800138000", DataCoding: 0, Payload: []byte("part"),
		TLVs: []smpp.TLV{
			{Tag: smpp.TagSARMsgRefNum, Value: []byte{0x12, 0x34}},
			{Tag: smpp.TagSARTotalSegments, Value: []byte{0x02}},
			{Tag: smpp.TagSARSegmentSeqnum, Value: []byte{0x01}},
		},
	}
	env := smppSubmitEnvelope("session-1", "esme", submit)
	if !env.SARSet || string(env.SARRefNum) != string([]byte{0x12, 0x34}) || string(env.SARTotalSegments) != "\x02" || string(env.SARSegmentSeqnum) != "\x01" {
		t.Fatalf("SAR metadata not preserved: %+v", env)
	}
}

func TestSMPPSubmitEncodingPreservesExplicitDCS(t *testing.T) {
	tests := []struct {
		dcs  uint8
		want string
	}{{0x00, "gsm7"}, {0x03, "8bit"}, {0x08, "ucs2"}, {0xf5, "8bit"}}
	for _, test := range tests {
		if got := smppSubmitEncoding(test.dcs); got != test.want {
			t.Fatalf("DCS 0x%02x encoding=%q, want %q", test.dcs, got, test.want)
		}
	}
}

func TestSMPPSubmitEnvelopePreservesRawPayload(t *testing.T) {
	submit := smpp.SubmitSM{
		From: "1069", To: "8613800138000", Text: "", DataCoding: 0,
		Payload: []byte{0x1b, 0x65}, UDH: []byte{0x03, 0x70, 0x01, 0xff},
	}
	env := smppSubmitEnvelope("session-1", "esme", submit)
	if env.Encoding != "gsm7" || !env.RawPayloadSet || string(env.RawPayload) != string(submit.Payload) {
		t.Fatalf("raw SMPP payload not preserved: %+v", env)
	}
	env.RawPayload[0] = 0xff
	env.UDH[0] = 0xff
	if submit.Payload[0] != 0x1b || submit.UDH[0] != 0x03 {
		t.Fatal("envelope aliases parsed submit buffers")
	}
}

func TestDCS0PCAPSamplesStayByteExact(t *testing.T) {
	for _, payload := range [][]byte{{0x09}, {0x60}, {0x1b, 0x65}} {
		parsed, err := smpp.ParseSubmitSM(smpp.PDU{
			CommandID:  smpp.CommandSubmitSM,
			SequenceID: 2,
			Body:       testSubmitBody(0x00, payload),
		})
		if err != nil {
			t.Fatalf("parse payload %x: %v", payload, err)
		}
		env := smppSubmitEnvelope("session-1", "esme", parsed)
		parts := smppclient.BuildSubmitSM(smppclient.Message{
			GatewayID: env.ClientMsgID, SourceAddr: env.From, DestAddr: env.To, Text: env.Text,
			DataCoding: env.DataCoding, Encoding: env.Encoding, RawPayload: env.RawPayload, RawPayloadSet: env.RawPayloadSet,
		}, config.DefaultSMPPClientConfig())
		if len(parts) != 1 {
			t.Fatalf("payload %x produced %d parts", payload, len(parts))
		}
		forwarded, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: 3, Body: parts[0].Body})
		if err != nil {
			t.Fatalf("parse forwarded payload %x: %v", payload, err)
		}
		if forwarded.DataCoding != 0 || string(forwarded.Payload) != string(payload) {
			t.Fatalf("payload changed: input=%x output=%x dcs=0x%02x", payload, forwarded.Payload, forwarded.DataCoding)
		}
	}
}

func testSubmitBody(dataCoding byte, payload []byte) []byte {
	body := smpp.CString("")
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString("1069")...)
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString("8613800138000")...)
	body = append(body, 0x00, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	body = append(body, smpp.CString("")...)
	body = append(body, 0x00, 0x00, dataCoding, 0x00, byte(len(payload)))
	return append(body, payload...)
}
