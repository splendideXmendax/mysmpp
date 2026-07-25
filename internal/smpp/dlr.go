package smpp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

const (
	tlvReceiptedMessageID uint16 = 0x001E
	tlvMessageState       uint16 = 0x0427
)

type DLRParams struct {
	GatewayID    string
	SourceAddr   string
	DestAddr     string
	SubmittedAt  time.Time
	DoneAt       time.Time
	State        string
	ErrorCode    int
	OriginalText string
}

func BuildDLR(params DLRParams) PDU {
	body := buildDeliverSMBody(params)
	body = appendTLVCString(body, tlvReceiptedMessageID, params.GatewayID)
	body = appendTLVBytes(body, tlvMessageState, []byte{stateToCode(params.State)})
	return PDU{CommandID: commandDeliverSM, Status: statusOK, Body: body}
}

func FormatReceiptText(params DLRParams) string {
	state := normalizeState(params.State)
	delivered := 0
	if state == "DELIVRD" {
		delivered = 1
	}
	return fmt.Sprintf(
		"id:%s sub:001 dlvrd:%03d submit date:%s done date:%s stat:%s err:%03d text:%s",
		params.GatewayID,
		delivered,
		params.SubmittedAt.UTC().Format("0601021504"),
		params.DoneAt.UTC().Format("0601021504"),
		state,
		params.ErrorCode,
		truncateRunes(params.OriginalText, 20),
	)
}

func buildDeliverSMBody(params DLRParams) []byte {
	receipt := []byte(FormatReceiptText(params))
	var body []byte
	body = append(body, CString("")...)
	body = append(body, 0x00, 0x00)
	body = append(body, CString(params.SourceAddr)...)
	body = append(body, 0x00, 0x00)
	body = append(body, CString(params.DestAddr)...)
	body = append(body, 0x04, 0x00, 0x00)
	body = append(body, CString("")...)
	body = append(body, CString("")...)
	body = append(body, 0x00, 0x00, 0x00, 0x00)
	body = append(body, byte(len(receipt)))
	body = append(body, receipt...)
	return body
}

func appendTLVCString(body []byte, tag uint16, value string) []byte {
	return appendTLVBytes(body, tag, CString(value))
}

func appendTLVBytes(body []byte, tag uint16, value []byte) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, tag)
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(value)))
	buf.Write(value)
	return append(body, buf.Bytes()...)
}

func normalizeState(state string) string {
	switch state {
	case "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "ACCEPTD", "UNKNOWN", "REJECTD":
		return state
	default:
		return "UNKNOWN"
	}
}

func stateToCode(state string) uint8 {
	switch state {
	case "ENROUTE":
		return 1
	case "DELIVRD":
		return 2
	case "EXPIRED":
		return 3
	case "DELETED":
		return 4
	case "UNDELIV":
		return 5
	case "ACCEPTD":
		return 6
	case "REJECTD":
		return 8
	default:
		return 7
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
