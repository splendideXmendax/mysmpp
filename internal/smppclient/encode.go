package smppclient

import (
	"encoding/binary"
	"strings"
	"unicode"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

type submitPart struct {
	Body []byte
}

func BuildSubmitSM(msg Message, cfg config.SMPPClientConfig) []submitPart {
	encoding := msg.Encoding
	if encoding == "" || encoding == "auto" {
		encoding = message.DetectEncoding(msg.Text)
	}
	dataCoding := msg.DataCoding
	if dataCoding == 0 && strings.EqualFold(encoding, "ucs2") {
		dataCoding = 0x08
	}
	if dataCoding == 0x08 {
		encoding = "ucs2"
	} else if dataCoding == 0x03 {
		encoding = "8bit"
	}
	registeredDelivery := msg.RegisteredDelivery
	if cfg.RegisteredDelivery >= 0 {
		registeredDelivery = byte(cfg.RegisteredDelivery)
	}

	if len(msg.UDH) > 0 {
		payload := encodeText(msg.Text, dataCoding, cfg.GSM7Packing)
		params := submitBodyParams{
			Cfg:                cfg,
			Msg:                msg,
			DataCoding:         dataCoding,
			RegisteredDelivery: registeredDelivery,
			ShortMessage:       payload,
			SourceTON:          sourceTON(msg.SourceAddr, cfg.SourceTON),
			SourceNPI:          sourceNPI(msg.SourceAddr, cfg.SourceNPI),
			DestTON:            byte(cfg.DestTON),
			DestNPI:            byte(cfg.DestNPI),
		}
		if cfg.LongMessage == "sar" {
			if info, ok := smpp.ParseConcat(msg.UDH); ok {
				params.TLVs = []tlv{
					{Tag: smpp.TagSARMsgRefNum, Value: []byte{byte(info.Reference >> 8), byte(info.Reference)}},
					{Tag: smpp.TagSARTotalSegments, Value: []byte{info.Total}},
					{Tag: smpp.TagSARSegmentSeqnum, Value: []byte{info.Part}},
				}
				return []submitPart{{Body: buildSubmitBody(params)}}
			}
		}
		params.ESMClass = 0x40
		params.ShortMessage = append(append([]byte(nil), msg.UDH...), payload...)
		if cfg.LongMessage == "udh" && len(params.ShortMessage) > 254 {
			return buildSplitSubmitSM(msg, cfg, encoding, dataCoding, registeredDelivery)
		}
		return []submitPart{{Body: buildSubmitBody(params)}}
	}

	if cfg.LongMessage == "payload" {
		return []submitPart{{
			Body: buildSubmitBody(submitBodyParams{
				Cfg:                cfg,
				Msg:                msg,
				DataCoding:         dataCoding,
				RegisteredDelivery: registeredDelivery,
				ShortMessage:       nil,
				MessagePayload:     encodeText(msg.Text, dataCoding, cfg.GSM7Packing),
				SourceTON:          sourceTON(msg.SourceAddr, cfg.SourceTON),
				SourceNPI:          sourceNPI(msg.SourceAddr, cfg.SourceNPI),
				DestTON:            byte(cfg.DestTON),
				DestNPI:            byte(cfg.DestNPI),
				ESMClass:           0,
			}),
		}}
	}

	segments := message.Split(msg.Text, message.SplitOptions{ForceEncoding: encoding})
	out := make([]submitPart, 0, len(segments))
	for _, segment := range segments {
		payload := encodeText(segment.Text, dataCoding, cfg.GSM7Packing)
		esmClass := byte(0)
		tlvs := []tlv{}
		if len(segments) > 1 {
			switch cfg.LongMessage {
			case "sar":
				tlvs = append(tlvs,
					tlv{Tag: smpp.TagSARMsgRefNum, Value: []byte{byte(segment.Reference >> 8), byte(segment.Reference)}},
					tlv{Tag: smpp.TagSARTotalSegments, Value: []byte{byte(segment.Total)}},
					tlv{Tag: smpp.TagSARSegmentSeqnum, Value: []byte{byte(segment.Part)}},
				)
			default:
				esmClass = 0x40
				payload = append(append([]byte(nil), segment.UDH...), payload...)
			}
		}
		out = append(out, submitPart{Body: buildSubmitBody(submitBodyParams{
			Cfg:                cfg,
			Msg:                msg,
			DataCoding:         dataCoding,
			RegisteredDelivery: registeredDelivery,
			ShortMessage:       payload,
			TLVs:               tlvs,
			SourceTON:          sourceTON(msg.SourceAddr, cfg.SourceTON),
			SourceNPI:          sourceNPI(msg.SourceAddr, cfg.SourceNPI),
			DestTON:            byte(cfg.DestTON),
			DestNPI:            byte(cfg.DestNPI),
			ESMClass:           esmClass,
		})})
	}
	return out
}

func buildSplitSubmitSM(msg Message, cfg config.SMPPClientConfig, encoding string, dataCoding, registeredDelivery byte) []submitPart {
	segments := message.Split(msg.Text, message.SplitOptions{ForceEncoding: encoding})
	out := make([]submitPart, 0, len(segments))
	for _, segment := range segments {
		payload := encodeText(segment.Text, dataCoding, cfg.GSM7Packing)
		payload = append(append([]byte(nil), segment.UDH...), payload...)
		out = append(out, submitPart{Body: buildSubmitBody(submitBodyParams{
			Cfg:                cfg,
			Msg:                msg,
			DataCoding:         dataCoding,
			RegisteredDelivery: registeredDelivery,
			ShortMessage:       payload,
			SourceTON:          sourceTON(msg.SourceAddr, cfg.SourceTON),
			SourceNPI:          sourceNPI(msg.SourceAddr, cfg.SourceNPI),
			DestTON:            byte(cfg.DestTON),
			DestNPI:            byte(cfg.DestNPI),
			ESMClass:           0x40,
		})})
	}
	return out
}

func maxUDHShortMessageLen(dataCoding byte, packing string) int {
	switch dataCoding {
	case 0x03, 0x08:
		return 140
	case 0x00:
		if packing == "packed" {
			return 140
		}
		return 159
	default:
		return 140
	}
}

type submitBodyParams struct {
	Cfg                config.SMPPClientConfig
	Msg                Message
	DataCoding         byte
	RegisteredDelivery byte
	ShortMessage       []byte
	MessagePayload     []byte
	TLVs               []tlv
	SourceTON          byte
	SourceNPI          byte
	DestTON            byte
	DestNPI            byte
	ESMClass           byte
}

type tlv struct {
	Tag   uint16
	Value []byte
}

func buildSubmitBody(params submitBodyParams) []byte {
	body := []byte{}
	body = append(body, smpp.CString(params.Cfg.ServiceType)...)
	body = append(body, params.SourceTON, params.SourceNPI)
	body = append(body, smpp.CString(stripPlus(params.Msg.SourceAddr))...)
	body = append(body, params.DestTON, params.DestNPI)
	body = append(body, smpp.CString(stripPlus(params.Msg.DestAddr))...)
	body = append(body, params.ESMClass, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	body = append(body, smpp.CString(params.Cfg.ValidityPeriod)...)
	body = append(body, params.RegisteredDelivery, 0x00, params.DataCoding, 0x00)
	if len(params.ShortMessage) > 254 {
		body = append(body, 0x00)
		params.MessagePayload = params.ShortMessage
	} else {
		body = append(body, byte(len(params.ShortMessage)))
		body = append(body, params.ShortMessage...)
	}
	if len(params.MessagePayload) > 0 {
		params.TLVs = append(params.TLVs, tlv{Tag: smpp.TagMessagePayload, Value: params.MessagePayload})
	}
	for _, item := range params.TLVs {
		var header [4]byte
		binary.BigEndian.PutUint16(header[0:2], item.Tag)
		binary.BigEndian.PutUint16(header[2:4], uint16(len(item.Value)))
		body = append(body, header[:]...)
		body = append(body, item.Value...)
	}
	return body
}

func encodeText(text string, dataCoding byte, packing string) []byte {
	if dataCoding == 0x08 {
		return message.EncodeText(text, dataCoding)
	}
	if packing == "packed" {
		return message.EncodeText(text, dataCoding)
	}
	return message.EncodeGSM7Unpacked(text)
}

func sourceTON(addr string, configured int) byte {
	if configured >= 0 {
		return byte(configured)
	}
	if hasLetter(addr) {
		return 5
	}
	digits := strings.TrimPrefix(addr, "+")
	if strings.HasPrefix(addr, "+") || len(digits) > 8 {
		return 1
	}
	return 3
}

func sourceNPI(addr string, configured int) byte {
	if configured >= 0 {
		return byte(configured)
	}
	if hasLetter(addr) || len(strings.TrimPrefix(addr, "+")) <= 8 {
		return 0
	}
	return 1
}

func hasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func stripPlus(value string) string {
	return strings.TrimPrefix(value, "+")
}
