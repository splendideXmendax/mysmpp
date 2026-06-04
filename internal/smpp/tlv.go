package smpp

import "encoding/binary"

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
	var out []TLV
	for len(body) >= 4 {
		tag := binary.BigEndian.Uint16(body[:2])
		size := int(binary.BigEndian.Uint16(body[2:4]))
		if size < 0 || 4+size > len(body) {
			break
		}
		value := append([]byte(nil), body[4:4+size]...)
		out = append(out, TLV{Tag: tag, Value: value})
		body = body[4+size:]
	}
	return out
}

func FindTLV(tlvs []TLV, tag uint16) ([]byte, bool) {
	for _, tlv := range tlvs {
		if tlv.Tag == tag {
			return tlv.Value, true
		}
	}
	return nil, false
}
