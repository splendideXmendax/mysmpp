package config

import "testing"

func TestNormalizeBackfillsServerEnquirePeriod(t *testing.T) {
	cfg := Default()
	cfg.SMPP.EnquirePeriod = ""
	cfg.Normalize()
	if cfg.SMPP.EnquirePeriod != "30s" {
		t.Fatalf("expected default enquire_period, got %q", cfg.SMPP.EnquirePeriod)
	}
}

func TestNormalizePreservesExplicitEnquirePeriod(t *testing.T) {
	cfg := Default()
	cfg.SMPP.EnquirePeriod = "45s"
	cfg.Normalize()
	if cfg.SMPP.EnquirePeriod != "45s" {
		t.Fatalf("expected explicit enquire_period, got %q", cfg.SMPP.EnquirePeriod)
	}
}
