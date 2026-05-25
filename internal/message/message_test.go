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
	if Join(segments) != text {
		t.Fatal("joined text does not match original")
	}
}

func TestDetectUCS2(t *testing.T) {
	if got := DetectEncoding("你好"); got != "ucs2" {
		t.Fatalf("expected ucs2, got %s", got)
	}
}
