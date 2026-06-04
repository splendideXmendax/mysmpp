package smpp

import "errors"

type ConcatInfo struct {
	Reference uint16
	Total     uint8
	Part      uint8
}

func SplitUDH(raw []byte, esmClass uint8) (udh, body []byte, err error) {
	if esmClass&0x40 == 0 {
		return nil, raw, nil
	}
	if len(raw) == 0 {
		return nil, nil, errors.New("udh flag set but short_message empty")
	}
	udhLen := int(raw[0]) + 1
	if udhLen > len(raw) {
		return nil, nil, errors.New("udh length exceeds short_message")
	}
	return append([]byte(nil), raw[:udhLen]...), raw[udhLen:], nil
}

func ParseConcat(udh []byte) (ConcatInfo, bool) {
	if len(udh) == 0 {
		return ConcatInfo{}, false
	}
	udhLen := int(udh[0])
	if 1+udhLen > len(udh) {
		return ConcatInfo{}, false
	}
	ies := udh[1 : 1+udhLen]
	for len(ies) >= 2 {
		iei := ies[0]
		iedl := int(ies[1])
		if 2+iedl > len(ies) {
			break
		}
		data := ies[2 : 2+iedl]
		switch iei {
		case 0x00:
			if len(data) == 3 {
				return ConcatInfo{Reference: uint16(data[0]), Total: data[1], Part: data[2]}, true
			}
		case 0x08:
			if len(data) == 4 {
				return ConcatInfo{Reference: uint16(data[0])<<8 | uint16(data[1]), Total: data[2], Part: data[3]}, true
			}
		}
		ies = ies[2+iedl:]
	}
	return ConcatInfo{}, false
}

func ParseSAR(tlvs []TLV) (ConcatInfo, bool) {
	ref, okRef := FindTLV(tlvs, TagSARMsgRefNum)
	total, okTotal := FindTLV(tlvs, TagSARTotalSegments)
	part, okPart := FindTLV(tlvs, TagSARSegmentSeqnum)
	if !okRef || !okTotal || !okPart || len(total) == 0 || len(part) == 0 {
		return ConcatInfo{}, false
	}
	var refNum uint16
	switch len(ref) {
	case 1:
		refNum = uint16(ref[0])
	default:
		refNum = uint16(ref[0])<<8 | uint16(ref[1])
	}
	return ConcatInfo{Reference: refNum, Total: total[0], Part: part[0]}, true
}
