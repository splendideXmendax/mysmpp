package dispatch

import (
	"errors"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

var ErrInvalidDestAddr = errors.New("invalid destination address")

type destAddrOptions struct {
	AllowShortCode bool
	MinShortLen    int
	MaxShortLen    int
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
	if _, ok := splitE164CountryCode(s); !ok {
		return ErrInvalidDestAddr
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

var e164CountryCodes = map[string]struct{}{
	"1": {}, "7": {},
	"20": {}, "27": {}, "30": {}, "31": {}, "32": {}, "33": {}, "34": {}, "36": {}, "39": {},
	"40": {}, "41": {}, "43": {}, "44": {}, "45": {}, "46": {}, "47": {}, "48": {}, "49": {},
	"51": {}, "52": {}, "53": {}, "54": {}, "55": {}, "56": {}, "57": {}, "58": {},
	"60": {}, "61": {}, "62": {}, "63": {}, "64": {}, "65": {}, "66": {},
	"81": {}, "82": {}, "84": {}, "86": {}, "90": {}, "91": {}, "92": {}, "93": {}, "94": {}, "95": {}, "98": {},
	"211": {}, "212": {}, "213": {}, "216": {}, "218": {}, "220": {}, "221": {}, "222": {}, "223": {}, "224": {},
	"225": {}, "226": {}, "227": {}, "228": {}, "229": {}, "230": {}, "231": {}, "232": {}, "233": {}, "234": {},
	"235": {}, "236": {}, "237": {}, "238": {}, "239": {}, "240": {}, "241": {}, "242": {}, "243": {}, "244": {},
	"245": {}, "246": {}, "248": {}, "249": {}, "250": {}, "251": {}, "252": {}, "253": {}, "254": {}, "255": {},
	"256": {}, "257": {}, "258": {}, "260": {}, "261": {}, "262": {}, "263": {}, "264": {}, "265": {}, "266": {},
	"267": {}, "268": {}, "269": {}, "290": {}, "291": {}, "297": {}, "298": {}, "299": {},
	"350": {}, "351": {}, "352": {}, "353": {}, "354": {}, "355": {}, "356": {}, "357": {}, "358": {}, "359": {},
	"370": {}, "371": {}, "372": {}, "373": {}, "374": {}, "375": {}, "376": {}, "377": {}, "378": {}, "379": {},
	"380": {}, "381": {}, "382": {}, "383": {}, "385": {}, "386": {}, "387": {}, "389": {},
	"420": {}, "421": {}, "423": {},
	"500": {}, "501": {}, "502": {}, "503": {}, "504": {}, "505": {}, "506": {}, "507": {}, "508": {}, "509": {},
	"590": {}, "591": {}, "592": {}, "593": {}, "594": {}, "595": {}, "596": {}, "597": {}, "598": {}, "599": {},
	"670": {}, "672": {}, "673": {}, "674": {}, "675": {}, "676": {}, "677": {}, "678": {}, "679": {},
	"680": {}, "681": {}, "682": {}, "683": {}, "685": {}, "686": {}, "687": {}, "688": {}, "689": {},
	"690": {}, "691": {}, "692": {},
	"800": {}, "808": {}, "850": {}, "852": {}, "853": {}, "855": {}, "856": {}, "870": {}, "878": {}, "880": {}, "881": {},
	"882": {}, "883": {}, "886": {}, "888": {},
	"960": {}, "961": {}, "962": {}, "963": {}, "964": {}, "965": {}, "966": {}, "967": {}, "968": {},
	"970": {}, "971": {}, "972": {}, "973": {}, "974": {}, "975": {}, "976": {}, "977": {}, "979": {}, "992": {},
	"993": {}, "994": {}, "995": {}, "996": {}, "998": {},
}
