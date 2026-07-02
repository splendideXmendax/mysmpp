package filter

import (
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestEngineBlocksNormalizedKeyword(t *testing.T) {
	e, err := Compile(config.FilterConfig{
		Enabled:   true,
		Normalize: config.NormalizeConfig{Lowercase: true, FullToHalf: true, StripZeroWidth: true},
		Rules: []config.FilterRule{{
			Name:     "gambling",
			Keywords: []string{"赌场"},
			Action:   "block",
			Priority: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := e.Evaluate("赌\u200b场")
	if decision.Action != ActionBlock || decision.Reason != "gambling" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestEngineMasksWithoutLowercasingOriginal(t *testing.T) {
	e, err := Compile(config.FilterConfig{
		Enabled:   true,
		Normalize: config.NormalizeConfig{Lowercase: true},
		Rules: []config.FilterRule{{
			Name:     "url",
			Keywords: []string{"promo"},
			Action:   "mask",
			MaskWith: "[x]",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := e.Evaluate("Use PROMO Now")
	if decision.Action != ActionMask {
		t.Fatalf("expected mask, got %+v", decision)
	}
	if decision.NewText != "Use [x] Now" {
		t.Fatalf("original case should be preserved outside mask, got %q", decision.NewText)
	}
}

func TestEngineCollectsTags(t *testing.T) {
	e, err := Compile(config.FilterConfig{
		Enabled:   true,
		Normalize: config.NormalizeConfig{Lowercase: true},
		Rules: []config.FilterRule{{
			Name:     "mkt",
			Keywords: []string{"promo"},
			Action:   "tag",
			Tag:      "marketing",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := e.Evaluate("promo")
	if decision.Action != ActionPass {
		t.Fatalf("tag-only should pass, got %+v", decision)
	}
	if _, ok := decision.Tags["marketing"]; !ok {
		t.Fatalf("missing marketing tag: %+v", decision.Tags)
	}
}
