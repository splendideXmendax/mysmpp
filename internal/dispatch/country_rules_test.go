package dispatch

import (
	"strings"
	"testing"
)

func TestGeneratedCountryRules(t *testing.T) {
	if len(e164CountryCodes) != 216 {
		t.Fatalf("country code count=%d, want 216", len(e164CountryCodes))
	}
	if len(countryMaxTotalLength) != 51 {
		t.Fatalf("country length rule count=%d, want 51", len(countryMaxTotalLength))
	}
	if countryMaxTotalLength["86"] != 13 {
		t.Fatalf("China max total length=%d, want 13", countryMaxTotalLength["86"])
	}
	if countryMaxTotalLength["856"] != 15 {
		t.Fatalf("Laos max total length=%d, want E.164 cap 15", countryMaxTotalLength["856"])
	}
	if _, ok := e164CountryCodes["247"]; !ok {
		t.Fatal("Ascension Island calling code 247 must be recognized")
	}
	if publicCountrySourceSHA256 == "" || countryLengthSourceSHA256 == "" {
		t.Fatal("country rule source fingerprints must be recorded")
	}
}

func TestValidateCountryLengthBoundaries(t *testing.T) {
	tests := []struct {
		name string
		addr string
		mode string
		ok   bool
	}{
		{name: "China exact max", addr: "8613800138000", ok: true},
		{name: "China over max", addr: "86138001380000"},
		{name: "NANP subdivision uses country one", addr: "12425551234", ok: true},
		{name: "NANP over max", addr: "124255512345"},
		{name: "Laos exact E164 cap", addr: "856123456789012", ok: true},
		{name: "Laos over E164 cap", addr: "8561234567890123"},
		{name: "uncovered compat", addr: "246123456789", ok: true},
		{name: "uncovered strict", addr: "246123456789", mode: "strict"},
		{name: "Ascension Island compat", addr: "2471234", ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode := test.mode
			if mode == "" {
				mode = "compat"
			}
			err := validateDestAddr(test.addr, destAddrOptions{CountryLengthMode: mode})
			if test.ok && err != nil {
				t.Fatalf("expected valid address, got %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("expected invalid address")
			}
		})
	}
}

func TestGeneratedCountryCodesAreUnambiguous(t *testing.T) {
	for code := range e164CountryCodes {
		if len(code) < 1 || len(code) > 3 || !digitsOnly(code) {
			t.Fatalf("invalid generated country code %q", code)
		}
		parsed, ok := splitE164CountryCode(code + "1")
		if !ok || parsed != code {
			t.Fatalf("country code %q parsed as %q, ok=%v", code, parsed, ok)
		}
		for other := range e164CountryCodes {
			if code != other && strings.HasPrefix(other, code) {
				t.Fatalf("country codes %q and %q have ambiguous prefixes", code, other)
			}
		}
	}
}

func TestAllCountryLengthRuleBoundaries(t *testing.T) {
	for code, maxLen := range countryMaxTotalLength {
		t.Run(code, func(t *testing.T) {
			if _, ok := e164CountryCodes[code]; !ok {
				t.Fatalf("length rule references unknown country code %q", code)
			}
			if maxLen <= len(code) || maxLen > 15 {
				t.Fatalf("invalid maximum total length %d for country code %q", maxLen, code)
			}
			atMax := code + strings.Repeat("1", maxLen-len(code))
			if err := validateDestAddr(atMax, destAddrOptions{CountryLengthMode: "strict"}); err != nil {
				t.Fatalf("maximum-length address %q rejected: %v", atMax, err)
			}
			overMax := atMax + "1"
			if err := validateDestAddr(overMax, destAddrOptions{CountryLengthMode: "strict"}); err == nil {
				t.Fatalf("overlength address %q accepted", overMax)
			}
		})
	}
}

func TestValidateDestAddrInternationalPrefixForms(t *testing.T) {
	if err := validateDestAddr("+8613800138000", destAddrOptions{CountryLengthMode: "compat"}); err != nil {
		t.Fatalf("leading plus should be accepted: %v", err)
	}
	if err := validateDestAddr("008613800138000", destAddrOptions{CountryLengthMode: "compat"}); err == nil {
		t.Fatal("international dialing prefix 00 is not an E.164 country code and should be rejected")
	}
}
