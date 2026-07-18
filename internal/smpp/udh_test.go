package smpp

import "testing"

func TestSplitUDHValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		ok   bool
	}{
		{name: "valid 16 bit concat", raw: []byte{0x06, 0x08, 0x04, 0x12, 0x34, 0x02, 0x01, 'a'}, ok: true},
		{name: "valid unknown element", raw: []byte{0x03, 0x70, 0x01, 0xff, 'a'}, ok: true},
		{name: "empty"},
		{name: "zero udhl", raw: []byte{0x00}},
		{name: "udhl exceeds body", raw: []byte{0x06, 0x08}},
		{name: "short ie header", raw: []byte{0x01, 0x08}},
		{name: "ie data exceeds udh", raw: []byte{0x03, 0x08, 0x04, 0x01}},
		{name: "wrong 8 bit concat length", raw: []byte{0x04, 0x00, 0x02, 0x01, 0x02}},
		{name: "zero concat total", raw: []byte{0x05, 0x00, 0x03, 0x7b, 0x00, 0x00}},
		{name: "concat part exceeds total", raw: []byte{0x05, 0x00, 0x03, 0x7b, 0x02, 0x03}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := SplitUDH(test.raw, 0x40)
			if test.ok && err != nil {
				t.Fatalf("expected valid UDH, got %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("expected invalid UDH")
			}
		})
	}
}

func TestParseConcatRejectsInvalidPartNumbers(t *testing.T) {
	for _, udh := range [][]byte{
		{0x05, 0x00, 0x03, 0x7b, 0x00, 0x00},
		{0x05, 0x00, 0x03, 0x7b, 0x02, 0x00},
		{0x05, 0x00, 0x03, 0x7b, 0x02, 0x03},
	} {
		if _, ok := ParseConcat(udh); ok {
			t.Fatalf("expected invalid concat UDH: % x", udh)
		}
	}
}

func TestParseSARRejectsMalformedValues(t *testing.T) {
	tests := [][]TLV{
		{{Tag: TagSARMsgRefNum}, {Tag: TagSARTotalSegments, Value: []byte{2}}, {Tag: TagSARSegmentSeqnum, Value: []byte{1}}},
		{{Tag: TagSARMsgRefNum, Value: []byte{1, 2, 3}}, {Tag: TagSARTotalSegments, Value: []byte{2}}, {Tag: TagSARSegmentSeqnum, Value: []byte{1}}},
		{{Tag: TagSARMsgRefNum, Value: []byte{1}}, {Tag: TagSARTotalSegments, Value: []byte{0}}, {Tag: TagSARSegmentSeqnum, Value: []byte{1}}},
		{{Tag: TagSARMsgRefNum, Value: []byte{1}}, {Tag: TagSARTotalSegments, Value: []byte{2}}, {Tag: TagSARSegmentSeqnum, Value: []byte{3}}},
	}
	for _, tlvs := range tests {
		if _, ok := ParseSAR(tlvs); ok {
			t.Fatalf("expected invalid SAR TLVs: %+v", tlvs)
		}
	}
}
