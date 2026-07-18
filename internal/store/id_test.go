package store

import "testing"

func TestParseGatewaySeqSupportsLegacyAndCurrentFormats(t *testing.T) {
	tests := []struct {
		id   string
		want uint64
		ok   bool
	}{
		{id: "g000000019328", want: 19328, ok: true},
		{id: "m0000eww", want: 19328, ok: true},
		{id: "x00000001", ok: false},
		{id: "m!", ok: false},
	}
	for _, test := range tests {
		got, ok := parseGatewaySeq(test.id)
		if ok != test.ok || got != test.want {
			t.Fatalf("parseGatewaySeq(%q) = (%d, %t), want (%d, %t)", test.id, got, ok, test.want, test.ok)
		}
	}
}
