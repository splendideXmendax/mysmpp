package filter

import (
	"strings"
	"unicode"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func normalizeText(text string, cfg config.NormalizeConfig) string {
	normalized, _ := normalizeTextWithMap(text, cfg)
	return normalized
}

func normalizeTextWithMap(text string, cfg config.NormalizeConfig) (string, []int) {
	var b strings.Builder
	normToOrig := []int{0}
	for origStart, r := range text {
		origEnd := origStart + len(string(r))
		if cfg.StripZeroWidth && isZeroWidth(r) {
			continue
		}
		if cfg.FullToHalf {
			r = fullToHalf(r)
		}
		if cfg.Lowercase {
			r = unicode.ToLower(r)
		}
		before := b.Len()
		b.WriteRune(r)
		written := b.Len() - before
		for i := 1; i <= written; i++ {
			if i == written {
				normToOrig = append(normToOrig, origEnd)
			} else {
				normToOrig = append(normToOrig, origStart)
			}
		}
	}
	return b.String(), normToOrig
}

func isZeroWidth(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\ufeff':
		return true
	default:
		return false
	}
}

func fullToHalf(r rune) rune {
	if r == '\u3000' {
		return ' '
	}
	if r >= '\uff01' && r <= '\uff5e' {
		return r - 0xfee0
	}
	return r
}
