package message

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSplitShortMessage(t *testing.T) {
	segments := Split("hello", SplitOptions{})
	if len(segments) != 1 {
		t.Fatalf("expected one segment, got %d", len(segments))
	}
	if segments[0].Text != "hello" {
		t.Fatalf("unexpected text %q", segments[0].Text)
	}
}

func TestSplitLongGSM7Message(t *testing.T) {
	text := ""
	for i := 0; i < 170; i++ {
		text += "a"
	}
	segments := Split(text, SplitOptions{})
	if len(segments) != 2 {
		t.Fatalf("expected two segments, got %d", len(segments))
	}
	if segments[0].Total != 2 || segments[1].Part != 2 {
		t.Fatalf("unexpected segment numbering: %+v", segments)
	}
	if len(segments[0].UDH) != 6 || segments[0].UDH[1] != 0x00 || segments[0].UDH[2] != 0x03 {
		t.Fatalf("expected 8-bit concat UDH, got % x", segments[0].UDH)
	}
	if Join(segments) != text {
		t.Fatal("joined text does not match original")
	}
}

func TestSplit8BitMessage(t *testing.T) {
	text := ""
	for i := 0; i < 200; i++ {
		text += "a"
	}
	segments := Split(text, SplitOptions{ForceEncoding: "8bit"})
	if len(segments) != 2 {
		t.Fatalf("expected two segments, got %d", len(segments))
	}
	if len(segments[0].UDH) != 6 {
		t.Fatalf("expected 6-byte UDH, got % x", segments[0].UDH)
	}
	if len([]rune(segments[0].Text)) != Default8BitConcatLimit {
		t.Fatalf("expected first 8bit segment length %d, got %d", Default8BitConcatLimit, len([]rune(segments[0].Text)))
	}
	if Join(segments) != text {
		t.Fatal("joined text does not match original")
	}
}

func TestSplitCountsGSM7ExtensionSeptets(t *testing.T) {
	text := ""
	for i := 0; i < 160; i++ {
		text += "{"
	}
	segments := Split(text, SplitOptions{})
	if len(segments) != 3 {
		t.Fatalf("expected three segments for 320 septets, got %d", len(segments))
	}
	if Join(segments) != text {
		t.Fatal("joined text does not match original")
	}
}

func TestDetectUCS2(t *testing.T) {
	if got := DetectEncoding("你好"); got != "ucs2" {
		t.Fatalf("expected ucs2, got %s", got)
	}
}

func TestDetectEncodingCoversGSM0338Alphabet(t *testing.T) {
	for r := range gsm7DefaultCodes {
		if got := DetectEncoding(string(r)); got != "gsm7" {
			t.Fatalf("default alphabet rune %q detected as %s", r, got)
		}
	}
	for r := range gsm7RuneToExt {
		if got := DetectEncoding(string(r)); got != "gsm7" {
			t.Fatalf("extension alphabet rune %q detected as %s", r, got)
		}
	}
	if got := DetectEncoding("`"); got != "ucs2" {
		t.Fatalf("GSM alphabet table-external rune detected as %s", got)
	}
}

func TestSplitUCS2ByUTF16CodeUnits(t *testing.T) {
	tests := []string{
		strings.Repeat("中", 70),
		strings.Repeat("中", 71),
		strings.Repeat("😀", 35),
		strings.Repeat("😀", 36),
		strings.Repeat("中", 66) + "😀",
		strings.Repeat("中", 200),
		strings.Repeat("😀", 120),
		"订单✅已发货📦请查收" + strings.Repeat("好", 100),
	}
	for _, text := range tests {
		segments := Split(text, SplitOptions{ForceEncoding: "ucs2"})
		for _, segment := range segments {
			payloadLen := len(utf16.Encode([]rune(segment.Text))) * 2
			if payloadLen+len(segment.UDH) > 140 {
				t.Fatalf("part %d/%d is oversized: payload=%d udh=%d", segment.Part, segment.Total, payloadLen, len(segment.UDH))
			}
		}
		if got := Join(segments); got != text {
			t.Fatalf("joined text mismatch: got %q want %q", got, text)
		}
	}
}

func TestUCS2CodecRoundTrip(t *testing.T) {
	text := "你好世界"
	encoded := EncodeText(text, 0x08)
	if len(encoded) != 8 {
		t.Fatalf("unexpected encoded length %d", len(encoded))
	}
	if got := DecodeText(encoded, 0x08); got != text {
		t.Fatalf("expected %q, got %q", text, got)
	}
}

func TestGSM7CodecRoundTrip(t *testing.T) {
	text := "hello @{}[]\\~^|€"
	encoded := EncodeText(text, 0x00)
	if string(encoded) == text {
		t.Fatal("gsm7 data should be packed, not raw ascii")
	}
	if got := DecodeText(encoded, 0x00); got != text {
		t.Fatalf("expected %q, got %q", text, got)
	}
}

func TestGSM7DecodeDropsSeptetPadding(t *testing.T) {
	text := "ABCDEFG"
	encoded := EncodeText(text, 0x00)
	if len(encoded) != 7 {
		t.Fatalf("expected 7 packed bytes, got %d", len(encoded))
	}
	if got := DecodeText(encoded, 0x00); got != text {
		t.Fatalf("expected %q without padding @, got %q", text, got)
	}
}

func TestDecodeSubmitTextAcceptsUnpackedASCIIForDefaultCoding(t *testing.T) {
	body := []byte("hello ascii")
	if got := DecodeSubmitText(body, 0x00); got != string(body) {
		t.Fatalf("expected unpacked ascii %q, got %q", body, got)
	}
}

func TestDecodeSubmitTextKeepsPackedGSM7ForDefaultCoding(t *testing.T) {
	text := "hello @{}[]\\~^|€"
	body := EncodeText(text, 0x00)
	if got := DecodeSubmitText(body, 0x00); got != text {
		t.Fatalf("expected packed gsm7 %q, got %q", text, got)
	}
}
