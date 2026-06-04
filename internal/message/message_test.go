package message

import "testing"

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
	if len(segments[0].UDH) != 7 || segments[0].UDH[1] != 0x08 || segments[0].UDH[2] != 0x04 {
		t.Fatalf("expected 16-bit concat UDH, got % x", segments[0].UDH)
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
