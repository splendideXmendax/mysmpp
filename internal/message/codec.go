package message

import "unicode/utf16"

func DecodeText(body []byte, dataCoding uint8) string {
	switch dataCoding {
	case 0x08:
		if len(body)%2 != 0 {
			return string(body)
		}
		u16 := make([]uint16, len(body)/2)
		for i := range u16 {
			u16[i] = uint16(body[i*2])<<8 | uint16(body[i*2+1])
		}
		return string(utf16.Decode(u16))
	case 0x03:
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		return string(runes)
	default:
		return decodeGSM7Packed(body)
	}
}

func EncodeText(text string, dataCoding uint8) []byte {
	switch dataCoding {
	case 0x08:
		u16 := utf16.Encode([]rune(text))
		out := make([]byte, len(u16)*2)
		for i, c := range u16 {
			out[i*2] = byte(c >> 8)
			out[i*2+1] = byte(c)
		}
		return out
	case 0x03:
		return []byte(text)
	default:
		return encodeGSM7Packed(text)
	}
}

var gsm7DefaultRunes = []rune{
	'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
	'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ', 'Σ', 'Θ', 'Ξ', 0, 'Æ', 'æ', 'ß', 'É',
	' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':', ';', '<', '=', '>', '?',
	'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
	'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
	'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
	'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
}

var gsm7DefaultCodes map[rune]byte
var gsm7ExtToRune = map[byte]rune{
	0x0A: '\f',
	0x14: '^',
	0x28: '{',
	0x29: '}',
	0x2F: '\\',
	0x3C: '[',
	0x3D: '~',
	0x3E: ']',
	0x40: '|',
	0x65: '€',
}
var gsm7RuneToExt map[rune]byte

func init() {
	gsm7DefaultCodes = map[rune]byte{}
	for i, r := range gsm7DefaultRunes {
		if r != 0 {
			gsm7DefaultCodes[r] = byte(i)
		}
	}
	gsm7RuneToExt = map[rune]byte{}
	for code, r := range gsm7ExtToRune {
		gsm7RuneToExt[r] = code
	}
}

func decodeGSM7Packed(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	septetCount := len(body) * 8 / 7
	septets := make([]byte, 0, septetCount)
	for i := 0; i < septetCount; i++ {
		bit := i * 7
		byteIndex := bit / 8
		shift := bit % 8
		if byteIndex >= len(body) {
			break
		}
		value := (body[byteIndex] >> shift) & 0x7F
		if shift > 1 && byteIndex+1 < len(body) {
			value |= (body[byteIndex+1] << (8 - shift)) & 0x7F
		}
		septets = append(septets, value)
	}
	out := make([]rune, 0, len(septets))
	for i := 0; i < len(septets); i++ {
		code := septets[i]
		if code == 0x1B && i+1 < len(septets) {
			i++
			if r, ok := gsm7ExtToRune[septets[i]]; ok {
				out = append(out, r)
			}
			continue
		}
		if int(code) < len(gsm7DefaultRunes) && gsm7DefaultRunes[code] != 0 {
			out = append(out, gsm7DefaultRunes[code])
		}
	}
	return string(out)
}

func encodeGSM7Packed(text string) []byte {
	septets := make([]byte, 0, len(text))
	for _, r := range text {
		if code, ok := gsm7DefaultCodes[r]; ok {
			septets = append(septets, code)
			continue
		}
		if code, ok := gsm7RuneToExt[r]; ok {
			septets = append(septets, 0x1B, code)
			continue
		}
		septets = append(septets, gsm7DefaultCodes['?'])
	}
	out := make([]byte, (len(septets)*7+7)/8)
	for i, septet := range septets {
		bit := i * 7
		byteIndex := bit / 8
		shift := bit % 8
		out[byteIndex] |= septet << shift
		if shift > 1 && byteIndex+1 < len(out) {
			out[byteIndex+1] |= septet >> (8 - shift)
		}
	}
	return out
}
