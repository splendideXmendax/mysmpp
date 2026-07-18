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
	if udhLen <= 1 || udhLen > len(raw) {
		return nil, nil, errors.New("udh length exceeds short_message")
	}
	for ies := raw[1:udhLen]; len(ies) > 0; {
		if len(ies) < 2 {
			return nil, nil, errors.New("malformed udh information element")
		}
		length := int(ies[1])
		if length > len(ies)-2 {
			return nil, nil, errors.New("malformed udh information element")
		}
		data := ies[2 : 2+length]
		switch ies[0] {
		case 0x00:
			if len(data) != 3 || !validConcatPart(data[1], data[2]) {
				return nil, nil, errors.New("malformed concatenation information element")
			}
		case 0x08:
			if len(data) != 4 || !validConcatPart(data[2], data[3]) {
				return nil, nil, errors.New("malformed concatenation information element")
			}
		}
		ies = ies[2+length:]
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
				return newConcatInfo(uint16(data[0]), data[1], data[2])
			}
		case 0x08:
			if len(data) == 4 {
				return newConcatInfo(uint16(data[0])<<8|uint16(data[1]), data[2], data[3])
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
	if !okRef || !okTotal || !okPart || (len(ref) != 1 && len(ref) != 2) || len(total) != 1 || len(part) != 1 {
		return ConcatInfo{}, false
	}
	var refNum uint16
	switch len(ref) {
	case 1:
		refNum = uint16(ref[0])
	case 2:
		refNum = uint16(ref[0])<<8 | uint16(ref[1])
	}
	return newConcatInfo(refNum, total[0], part[0])
}

func HasSAR(tlvs []TLV) bool {
	for _, item := range tlvs {
		switch item.Tag {
		case TagSARMsgRefNum, TagSARTotalSegments, TagSARSegmentSeqnum:
			return true
		}
	}
	return false
}

func newConcatInfo(reference uint16, total, part uint8) (ConcatInfo, bool) {
	if !validConcatPart(total, part) {
		return ConcatInfo{}, false
	}
	return ConcatInfo{Reference: reference, Total: total, Part: part}, true
}

func validConcatPart(total, part uint8) bool {
	return total > 0 && part > 0 && part <= total
}
