package smpp

import "testing"

func TestParseSubmitSM(t *testing.T) {
	body := []byte{}
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString("1069")...)
	body = append(body, 0x01, 0x01)
	body = append(body, CString("13800138000")...)
	body = append(body, 0x00, 0x00, 0x00)
	body = append(body, CString("")...)
	body = append(body, CString("")...)
	body = append(body, 0x01, 0x00, 0x00, 0x00)
	body = append(body, byte(len("hello")))
	body = append(body, []byte("hello")...)

	msg, err := parseSubmitSM(PDU{CommandID: commandSubmitSM, SequenceID: 7, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "1069" || msg.To != "13800138000" || msg.Text != "hello" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}
