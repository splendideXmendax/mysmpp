package smpp

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestBuildDLR(t *testing.T) {
	now := time.Date(2026, 6, 3, 15, 30, 0, 0, time.UTC)
	pdu := BuildDLR(DLRParams{
		GatewayID:    "g0000000001",
		SourceAddr:   "13800138000",
		DestAddr:     "1069",
		SubmittedAt:  now,
		DoneAt:       now.Add(time.Second),
		State:        "DELIVRD",
		ErrorCode:    0,
		OriginalText: "hello dlr",
	})

	if pdu.CommandID != commandDeliverSM {
		t.Fatalf("unexpected command id 0x%08x", pdu.CommandID)
	}
	offset := 0
	_ = readCString(pdu.Body, &offset)
	offset += 2
	from := readCString(pdu.Body, &offset)
	offset += 2
	to := readCString(pdu.Body, &offset)
	esmClass := pdu.Body[offset]
	offset += 3
	_ = readCString(pdu.Body, &offset)
	_ = readCString(pdu.Body, &offset)
	offset += 4
	smLen := int(pdu.Body[offset])
	offset++
	text := string(pdu.Body[offset : offset+smLen])
	offset += smLen

	if from != "13800138000" || to != "1069" {
		t.Fatalf("unexpected addresses %s -> %s", from, to)
	}
	if esmClass&0x04 == 0 {
		t.Fatalf("expected dlr esm_class bit, got 0x%02x", esmClass)
	}
	if !strings.Contains(text, "id:g0000000001") || !strings.Contains(text, "stat:DELIVRD") {
		t.Fatalf("unexpected receipt text %q", text)
	}
	if !hasTLV(pdu.Body[offset:], tlvReceiptedMessageID, CString("g0000000001")) {
		t.Fatal("missing receipted_message_id tlv")
	}
	if !hasTLV(pdu.Body[offset:], tlvMessageState, []byte{2}) {
		t.Fatal("missing message_state tlv")
	}
}

func TestBuildDLRUsesCurrentGatewayIDEverywhere(t *testing.T) {
	id := "m0000eww"
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	pdu := BuildDLR(DLRParams{GatewayID: id, SubmittedAt: now, DoneAt: now, State: "DELIVRD"})
	offset := 0
	_ = readCString(pdu.Body, &offset)
	offset += 2
	_ = readCString(pdu.Body, &offset)
	offset += 2
	_ = readCString(pdu.Body, &offset)
	offset += 3
	_ = readCString(pdu.Body, &offset)
	_ = readCString(pdu.Body, &offset)
	offset += 4
	smLen := int(pdu.Body[offset])
	offset++
	text := string(pdu.Body[offset : offset+smLen])
	offset += smLen
	if !strings.HasPrefix(text, "id:"+id+" ") {
		t.Fatalf("receipt text does not use gateway id: %q", text)
	}
	if !hasTLV(pdu.Body[offset:], tlvReceiptedMessageID, CString(id)) {
		t.Fatal("receipted_message_id TLV does not use gateway id")
	}
}

func hasTLV(body []byte, tag uint16, value []byte) bool {
	for len(body) >= 4 {
		gotTag := binary.BigEndian.Uint16(body[0:2])
		length := int(binary.BigEndian.Uint16(body[2:4]))
		body = body[4:]
		if length > len(body) {
			return false
		}
		if gotTag == tag && bytes.Equal(body[:length], value) {
			return true
		}
		body = body[length:]
	}
	return false
}
