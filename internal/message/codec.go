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
		return string(body)
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
	default:
		return []byte(text)
	}
}
