package smppclient

import (
	"strconv"
	"strings"
)

func NormalizeID(raw, format string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "\x00"))
	if raw == "" {
		return ""
	}
	switch strings.ToLower(format) {
	case "dec":
		trimmed := strings.TrimLeft(raw, "0")
		if trimmed == "" {
			return "0"
		}
		return trimmed
	case "hex":
		if v, err := strconv.ParseUint(raw, 16, 64); err == nil {
			return strconv.FormatUint(v, 10)
		}
		return raw
	default:
		if looksHexID(raw) {
			if v, err := strconv.ParseUint(raw, 16, 64); err == nil {
				return strconv.FormatUint(v, 10)
			}
		}
		return raw
	}
}

func looksHexID(value string) bool {
	hasHexLetter := false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
			hasHexLetter = true
		case r >= 'A' && r <= 'F':
			hasHexLetter = true
		default:
			return false
		}
	}
	return hasHexLetter
}
