package dispatch

import (
	"errors"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

var ErrInvalidDestAddr = errors.New("invalid destination address")
var ErrBlocked = errors.New("message blocked by content filter")
var ErrNoRoute = errors.New("no route matched")
var ErrTenantDisabled = errors.New("tenant is disabled")
var ErrRateExceeded = errors.New("tenant rate limit exceeded")
var ErrQuotaExceeded = store.ErrQuotaExceeded

type destAddrOptions struct {
	AllowShortCode    bool
	MinShortLen       int
	MaxShortLen       int
	CountryLengthMode string
}

func validateDestAddr(addr string, opts destAddrOptions) error {
	s := strings.TrimPrefix(addr, "+")
	if !digitsOnly(s) {
		return ErrInvalidDestAddr
	}
	if opts.AllowShortCode && isShortCode(s, opts) {
		return nil
	}
	if len(s) < 4 || len(s) > 15 {
		return ErrInvalidDestAddr
	}
	cc, ok := splitE164CountryCode(s)
	if !ok {
		return ErrInvalidDestAddr
	}
	switch opts.CountryLengthMode {
	case "", "off":
	case "strict":
		maxLen, ok := countryMaxTotalLength[cc]
		if !ok || len(s) > maxLen {
			return ErrInvalidDestAddr
		}
	case "compat":
		if maxLen, ok := countryMaxTotalLength[cc]; ok && len(s) > maxLen {
			return ErrInvalidDestAddr
		}
	}
	return nil
}

func rewriteDestAddr(addr string, rule config.AddrRewriteConfig) string {
	s := strings.TrimPrefix(addr, "+")
	if rule.StripTrunkZeroAfterCC {
		cc := rule.CountryCode
		if cc == "" {
			if parsed, ok := splitE164CountryCode(s); ok {
				cc = parsed
			}
		}
		if cc != "" && strings.HasPrefix(s, cc) {
			rest := strings.TrimLeft(s[len(cc):], "0")
			s = cc + rest
		}
	}
	if rule.AddPrefix != "" && !strings.HasPrefix(s, rule.AddPrefix) {
		s = rule.AddPrefix + s
	}
	return s
}

func splitE164CountryCode(number string) (string, bool) {
	for n := 1; n <= 3 && n <= len(number); n++ {
		cc := number[:n]
		if _, ok := e164CountryCodes[cc]; ok {
			return cc, true
		}
	}
	return "", false
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isShortCode(s string, opts destAddrOptions) bool {
	minLen := opts.MinShortLen
	if minLen <= 0 {
		minLen = 3
	}
	maxLen := opts.MaxShortLen
	if maxLen <= 0 {
		maxLen = 6
	}
	return len(s) >= minLen && len(s) <= maxLen
}
