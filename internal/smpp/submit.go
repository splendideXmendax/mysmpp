package smpp

import (
	"errors"

	"github.com/splendideXmendax/mysmpp/internal/message"
)

type SubmitSM struct {
	SequenceID           uint32
	From                 string
	To                   string
	Text                 string
	DataCoding           uint8
	RegisteredDelivery   uint8
	ESMClass             uint8
	ProtocolID           uint8
	PriorityFlag         uint8
	ReplaceIfPresent     uint8
	SMDefaultMsgID       uint8
	OptionalParamsOffset int
}

func ParseSubmitSM(pdu PDU) (SubmitSM, error) {
	offset := 0
	_ = readCString(pdu.Body, &offset) // service_type
	if offset+2 > len(pdu.Body) {
		return SubmitSM{}, errors.New("short submit_sm")
	}
	offset += 2 // source_addr_ton, source_addr_npi
	from := readCString(pdu.Body, &offset)
	if offset+2 > len(pdu.Body) {
		return SubmitSM{}, errors.New("missing destination ton/npi")
	}
	offset += 2
	to := readCString(pdu.Body, &offset)
	if offset+3 > len(pdu.Body) {
		return SubmitSM{}, errors.New("missing submit_sm fixed fields")
	}
	esmClass := pdu.Body[offset]
	offset++
	protocolID := pdu.Body[offset]
	offset++
	priorityFlag := pdu.Body[offset]
	offset++
	_ = readCString(pdu.Body, &offset)
	_ = readCString(pdu.Body, &offset)
	if offset+5 > len(pdu.Body) {
		return SubmitSM{}, errors.New("missing submit_sm delivery fields")
	}
	registeredDelivery := pdu.Body[offset]
	offset++
	replaceIfPresent := pdu.Body[offset]
	offset++
	dataCoding := pdu.Body[offset]
	offset++
	smDefaultMsgID := pdu.Body[offset]
	offset++
	if offset >= len(pdu.Body) {
		return SubmitSM{}, errors.New("missing sm length")
	}
	smLen := int(pdu.Body[offset])
	offset++
	if offset+smLen > len(pdu.Body) {
		return SubmitSM{}, errors.New("short short_message")
	}
	text := message.DecodeText(pdu.Body[offset:offset+smLen], dataCoding)
	offset += smLen
	return SubmitSM{
		SequenceID:           pdu.SequenceID,
		From:                 from,
		To:                   to,
		Text:                 text,
		DataCoding:           dataCoding,
		RegisteredDelivery:   registeredDelivery,
		ESMClass:             esmClass,
		ProtocolID:           protocolID,
		PriorityFlag:         priorityFlag,
		ReplaceIfPresent:     replaceIfPresent,
		SMDefaultMsgID:       smDefaultMsgID,
		OptionalParamsOffset: offset,
	}, nil
}

func parseSubmitSM(pdu PDU) (message.Message, error) {
	submit, err := ParseSubmitSM(pdu)
	if err != nil {
		return message.Message{}, err
	}
	msg := message.New("", message.DirectionMO, submit.From, submit.To, submit.Text)
	if submit.DataCoding == 0x08 {
		msg.Encoding = "ucs2"
	}
	return msg, nil
}
