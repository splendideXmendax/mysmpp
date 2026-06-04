package smpp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	commandBindReceiver        uint32 = 0x00000001
	commandBindTransmitter     uint32 = 0x00000002
	commandQuerySM             uint32 = 0x00000003
	commandSubmitSM            uint32 = 0x00000004
	commandDeliverSM           uint32 = 0x00000005
	commandUnbind              uint32 = 0x00000006
	commandBindTransceiver     uint32 = 0x00000009
	commandEnquireLink         uint32 = 0x00000015
	commandGenericNack         uint32 = 0x80000000
	commandBindReceiverResp    uint32 = 0x80000001
	commandBindTransmitterResp uint32 = 0x80000002
	commandQuerySMResp         uint32 = 0x80000003
	commandSubmitSMResp        uint32 = 0x80000004
	commandDeliverSMResp       uint32 = 0x80000005
	commandUnbindResp          uint32 = 0x80000006
	commandBindTransceiverResp uint32 = 0x80000009
	commandEnquireLinkResp     uint32 = 0x80000015

	statusOK           uint32 = 0x00000000
	statusInvalidCmd   uint32 = 0x00000003
	statusInvalidBind  uint32 = 0x00000004
	statusAlreadyBound uint32 = 0x00000005
	statusBindFailed   uint32 = 0x0000000D
	statusThrottled    uint32 = 0x00000058
)

type PDU struct {
	Length     uint32
	CommandID  uint32
	Status     uint32
	SequenceID uint32
	Body       []byte
}

func ReadPDU(r io.Reader) (PDU, error) {
	var header [16]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return PDU{}, err
	}
	length := binary.BigEndian.Uint32(header[0:4])
	if length < 16 {
		return PDU{}, errors.New("invalid pdu length")
	}
	if length > 1024*1024 {
		return PDU{}, fmt.Errorf("pdu too large: %d", length)
	}
	body := make([]byte, length-16)
	if _, err := io.ReadFull(r, body); err != nil {
		return PDU{}, err
	}
	return PDU{
		Length:     length,
		CommandID:  binary.BigEndian.Uint32(header[4:8]),
		Status:     binary.BigEndian.Uint32(header[8:12]),
		SequenceID: binary.BigEndian.Uint32(header[12:16]),
		Body:       body,
	}, nil
}

func WritePDU(w io.Writer, p PDU) error {
	length := uint32(16 + len(p.Body))
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, length)
	_ = binary.Write(&buf, binary.BigEndian, p.CommandID)
	_ = binary.Write(&buf, binary.BigEndian, p.Status)
	_ = binary.Write(&buf, binary.BigEndian, p.SequenceID)
	buf.Write(p.Body)
	_, err := w.Write(buf.Bytes())
	return err
}

func CString(value string) []byte {
	return append([]byte(value), 0x00)
}

func readCString(body []byte, offset *int) string {
	start := *offset
	for *offset < len(body) && body[*offset] != 0x00 {
		(*offset)++
	}
	value := string(body[start:*offset])
	if *offset < len(body) {
		(*offset)++
	}
	return value
}

func commandName(id uint32) string {
	switch id {
	case commandBindReceiver:
		return "bind_receiver"
	case commandBindTransmitter:
		return "bind_transmitter"
	case commandBindTransceiver:
		return "bind_transceiver"
	case commandSubmitSM:
		return "submit_sm"
	case commandDeliverSM:
		return "deliver_sm"
	case commandUnbind:
		return "unbind"
	case commandEnquireLink:
		return "enquire_link"
	default:
		return fmt.Sprintf("0x%08x", id)
	}
}
