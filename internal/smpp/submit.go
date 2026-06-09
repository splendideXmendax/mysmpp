package smpp

import (
	"errors"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

const smppMaxServiceType = 6

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
	Payload              []byte
	UDH                  []byte
	Concat               *ConcatInfo
	TLVs                 []TLV
}

func ParseSubmitSM(pdu PDU) (SubmitSM, error) {
	offset := 0
	if _, err := readCStringMax(pdu.Body, &offset, smppMaxServiceType); err != nil {
		return SubmitSM{}, err
	}
	if offset+2 > len(pdu.Body) {
		return SubmitSM{}, errors.New("short submit_sm")
	}
	offset += 2 // source_addr_ton, source_addr_npi
	from, err := readCStringMax(pdu.Body, &offset, config.SMPPMaxAddress)
	if err != nil {
		return SubmitSM{}, err
	}
	if offset+2 > len(pdu.Body) {
		return SubmitSM{}, errors.New("missing destination ton/npi")
	}
	offset += 2
	to, err := readCStringMax(pdu.Body, &offset, config.SMPPMaxAddress)
	if err != nil {
		return SubmitSM{}, err
	}
	if offset+3 > len(pdu.Body) {
		return SubmitSM{}, errors.New("missing submit_sm fixed fields")
	}
	esmClass := pdu.Body[offset]
	offset++
	protocolID := pdu.Body[offset]
	offset++
	priorityFlag := pdu.Body[offset]
	offset++
	if _, err := readCStringMax(pdu.Body, &offset, 16); err != nil {
		return SubmitSM{}, err
	}
	if _, err := readCStringMax(pdu.Body, &offset, 16); err != nil {
		return SubmitSM{}, err
	}
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
	var raw []byte
	if offset+smLen > len(pdu.Body) {
		return SubmitSM{}, errors.New("short short_message")
	}
	raw = append([]byte(nil), pdu.Body[offset:offset+smLen]...)
	offset += smLen
	tlvs := ParseTLVs(pdu.Body[offset:])
	if payload, ok := FindTLV(tlvs, TagMessagePayload); ok && len(payload) > 0 {
		raw = append([]byte(nil), payload...)
	}
	udh, body, err := SplitUDH(raw, esmClass)
	if err != nil {
		return SubmitSM{}, err
	}
	var concat *ConcatInfo
	if udh != nil {
		if parsed, ok := ParseConcat(udh); ok {
			concat = &parsed
		}
	}
	if concat == nil {
		if parsed, ok := ParseSAR(tlvs); ok {
			concat = &parsed
		}
	}
	text := message.DecodeSubmitText(body, dataCoding)
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
		Payload:              append([]byte(nil), body...),
		UDH:                  udh,
		Concat:               concat,
		TLVs:                 tlvs,
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
