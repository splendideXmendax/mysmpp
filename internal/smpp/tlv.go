package smpp

import (
	"encoding/binary"
	"errors"
)

const (
	TagSourcePort         uint16 = 0x020A
	TagDestPort           uint16 = 0x020B
	TagSARMsgRefNum       uint16 = 0x020C
	TagSARTotalSegments   uint16 = 0x020E
	TagSARSegmentSeqnum   uint16 = 0x020F
	TagMessagePayload     uint16 = 0x0424
	TagReceiptedMessageID uint16 = 0x001E
	TagMessageState       uint16 = 0x0427
)

type TLV struct {
	Tag   uint16
	Value []byte
}

func ParseTLVs(body []byte) []TLV {
	out, _ := ParseTLVsStrict(body)
	return out
}

func ParseTLVsStrict(body []byte) ([]TLV, error) {
	var out []TLV
	for len(body) > 0 {
		if len(body) < 4 {
			return nil, errors.New("short optional parameter header")
		}
		tag := binary.BigEndian.Uint16(body[:2])
		size := int(binary.BigEndian.Uint16(body[2:4]))
		if 4+size > len(body) {
			return nil, errors.New("short optional parameter value")
		}
		value := append([]byte(nil), body[4:4+size]...)
		out = append(out, TLV{Tag: tag, Value: value})
		body = body[4+size:]
	}
	return out, nil
}

func FindTLV(tlvs []TLV, tag uint16) ([]byte, bool) {
	for _, tlv := range tlvs {
		if tlv.Tag == tag {
			return tlv.Value, true
		}
	}
	return nil, false
}
