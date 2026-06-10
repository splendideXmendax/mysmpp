package smppclient

import (
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func TestBindBodyPreservesEmptySystemType(t *testing.T) {
	body := bindBody("acct", "secret", "")
	offset := 0
	if got := readCStringAt(body, &offset); got != "acct" {
		t.Fatalf("system_id = %q", got)
	}
	if got := readCStringAt(body, &offset); got != "secret" {
		t.Fatalf("password = %q", got)
	}
	if got := readCStringAt(body, &offset); got != "" {
		t.Fatalf("system_type = %q, want empty", got)
	}
}

func TestConnectionAttemptOutChannelIsScoped(t *testing.T) {
	cfg := Config{SMPP: config.DefaultSMPPClientConfig()}
	conn := newConnection(1, cfg, nil)

	stale := make(chan smpp.PDU)
	conn.setOut(stale)
	other := make(chan smpp.PDU)
	conn.clearOut(other)
	if conn.out != stale {
		t.Fatal("clearOut should ignore non-current attempt channel")
	}

	active := make(chan smpp.PDU)
	conn.setOut(active)
	conn.clearOut(stale)
	if conn.out != active {
		t.Fatal("current attempt channel was cleared by stale attempt")
	}
	conn.clearOut(active)
	if conn.out != nil {
		t.Fatal("current attempt channel should be cleared")
	}
}

func TestParseDeliverSMRejectsMO(t *testing.T) {
	body := buildDeliverBodyForTest(0x00, "id:not-a-receipt stat:DELIVRD")
	if _, ok, isReceipt := ParseDeliverSM(body, "auto", "auto"); ok || isReceipt {
		t.Fatalf("MO deliver_sm should not parse as DLR: ok=%v isReceipt=%v", ok, isReceipt)
	}
}

func readCStringAt(body []byte, offset *int) string {
	start := *offset
	for *offset < len(body) && body[*offset] != 0 {
		(*offset)++
	}
	value := string(body[start:*offset])
	if *offset < len(body) {
		(*offset)++
	}
	return value
}

func buildDeliverBodyForTest(esmClass byte, text string) []byte {
	body := []byte{}
	body = append(body, smpp.CString("")...)
	body = append(body, 0x00, 0x00)
	body = append(body, smpp.CString("13800138000")...)
	body = append(body, 0x00, 0x00)
	body = append(body, smpp.CString("10690000")...)
	body = append(body, esmClass, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	body = append(body, smpp.CString("")...)
	body = append(body, 0x00, 0x00, 0x00, 0x00)
	body = append(body, byte(len(text)))
	body = append(body, []byte(text)...)
	return body
}
