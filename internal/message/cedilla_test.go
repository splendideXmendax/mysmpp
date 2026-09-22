package message

import (
	"bytes"
	"strings"
	"testing"
)

func TestGSM7CedillaEncodingCompatibility(t *testing.T) {
	for _, tc := range []struct {
		text     string
		unpacked []byte
		packed   []byte
	}{
		{"ç", []byte{0x09}, []byte{0x09}},
		{"Ç", []byte{0x09}, []byte{0x09}},
		{"çÇ", []byte{0x09, 0x09}, []byte{0x89, 0x04}},
		{"c", []byte{0x63}, []byte{0x63}},
		{"C", []byte{0x43}, []byte{0x43}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := DetectEncoding(tc.text); got != "gsm7" {
				t.Fatalf("encoding=%q, want gsm7", got)
			}
			if got := EncodeGSM7Unpacked(tc.text); !bytes.Equal(got, tc.unpacked) {
				t.Fatalf("unpacked=%x, want %x", got, tc.unpacked)
			}
			if got := EncodeText(tc.text, 0); !bytes.Equal(got, tc.packed) {
				t.Fatalf("packed=%x, want %x", got, tc.packed)
			}
		})
	}
	// Encoding accepts an alias; changing the existing receive-side spelling
	// would independently affect inbound SMPP text and content filters.
	if got := DecodeText([]byte{0x09}, 0); got != "Ç" {
		t.Fatalf("legacy receive mapping changed: %q", got)
	}
	// The existing SMPP ASCII heuristic treats a lone 0x09 as a tab.
	// Raw SMPP forwarding must continue to preserve that byte unchanged.
	if got := DecodeSubmitText([]byte{0x09}, 0); got != "\t" {
		t.Fatalf("legacy ASCII heuristic changed: %q", got)
	}
	for _, tc := range []struct {
		text string
		wire []byte
	}{
		{"ç", []byte{0, 0xe7}},
		{"Ç", []byte{0, 0xc7}},
		{"ç中文", []byte{0, 0xe7, 0x4e, 0x2d, 0x65, 0x87}},
	} {
		if got := EncodeText(tc.text, 8); !bytes.Equal(got, tc.wire) || DecodeText(got, 8) != tc.text {
			t.Fatalf("explicit UCS2 changed for %q: %x", tc.text, got)
		}
	}
	for _, text := range []string{"ç中文", "ç😀", "ç\x00", "ç`", "c\u0327"} {
		if got := DetectEncoding(text); got != "ucs2" {
			t.Fatalf("out-of-alphabet text %q detected as %s", text, got)
		}
	}
}

func TestGSM7CedillaSegmentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		force string
		parts int
	}{
		{"single", strings.Repeat("ç", 160), "", 1},
		{"concatenated", strings.Repeat("ç", 161), "", 2},
		{"two-full", strings.Repeat("ç", 306), "", 2},
		{"three", strings.Repeat("ç", 307), "", 3},
		{"extension-single", strings.Repeat("ç", 158) + "€", "", 1},
		{"extension-overflow", strings.Repeat("ç", 159) + "€", "", 2},
		{"explicit-ucs2", strings.Repeat("ç", 71), "ucs2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segments := Split(tc.text, SplitOptions{ForceEncoding: tc.force})
			if len(segments) != tc.parts || Join(segments) != tc.text {
				t.Fatalf("split changed text or count: parts=%d want=%d", len(segments), tc.parts)
			}
			if tc.force == "" && tc.parts > 1 {
				for i, segment := range segments {
					if segment.Part != i+1 || segment.Total != tc.parts || len(EncodeGSM7Unpacked(segment.Text)) > 153 {
						t.Fatalf("invalid concatenated segment: %+v", segment)
					}
				}
			}
		})
	}
}
