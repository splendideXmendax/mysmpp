package message

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultGSM7SingleLimit = 160
	DefaultGSM7ConcatLimit = 153
	Default8BitSingleLimit = 140
	Default8BitConcatLimit = 134
	DefaultUCS2SingleLimit = 70
	DefaultUCS2ConcatLimit = 67
)

type Direction string

const (
	DirectionMO Direction = "mo"
	DirectionMT Direction = "mt"
)

type Message struct {
	ID          string
	ProviderID  string
	Direction   Direction
	From        string
	To          string
	Text        string
	Encoding    string
	Route       string
	Provider    string
	SourceKind  string
	SourceID    string
	State       string
	ErrorCode   int
	Segments    []Segment
	Metadata    map[string]string
	SubmittedAt time.Time
	SentAt      time.Time
	DoneAt      time.Time
}

type Segment struct {
	Reference uint16
	Part      int
	Total     int
	Text      string
	UDH       []byte
}

type SplitOptions struct {
	ForceEncoding string
}

func New(id string, direction Direction, from, to, text string) Message {
	return Message{
		ID:          id,
		Direction:   direction,
		From:        from,
		To:          to,
		Text:        text,
		Encoding:    DetectEncoding(text),
		State:       "submitted",
		Metadata:    map[string]string{},
		SubmittedAt: time.Now().UTC(),
	}
}

func DetectEncoding(text string) string {
	for _, r := range text {
		if !isGSM7Basic(r) {
			return "ucs2"
		}
	}
	return "gsm7"
}

func Split(text string, opts SplitOptions) []Segment {
	encoding := opts.ForceEncoding
	if encoding == "" || encoding == "auto" {
		encoding = DetectEncoding(text)
	}

	singleLimit := DefaultGSM7SingleLimit
	concatLimit := DefaultGSM7ConcatLimit
	switch {
	case strings.EqualFold(encoding, "8bit"):
		singleLimit = Default8BitSingleLimit
		concatLimit = Default8BitConcatLimit
	case strings.EqualFold(encoding, "ucs2"):
		singleLimit = DefaultUCS2SingleLimit
		concatLimit = DefaultUCS2ConcatLimit
	}

	textLen := utf8.RuneCountInString(text)
	if strings.EqualFold(encoding, "gsm7") {
		textLen = gsm7SeptetLen(text)
	}
	if textLen <= singleLimit {
		return []Segment{{Part: 1, Total: 1, Text: text}}
	}

	chunks := chunkRunes(text, concatLimit)
	if strings.EqualFold(encoding, "gsm7") {
		chunks = chunkGSM7(text, concatLimit)
	}
	ref := randomReference()
	segments := make([]Segment, 0, len(chunks))
	for i, chunk := range chunks {
		segments = append(segments, Segment{
			Reference: ref,
			Part:      i + 1,
			Total:     len(chunks),
			Text:      chunk,
			UDH:       concatUDH(ref, i+1, len(chunks)),
		})
	}
	return segments
}

func Join(segments []Segment) string {
	if len(segments) == 0 {
		return ""
	}
	ordered := make([]Segment, len(segments))
	copy(ordered, segments)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j-1].Part > ordered[j].Part; j-- {
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	var b strings.Builder
	for _, segment := range ordered {
		b.WriteString(segment.Text)
	}
	return b.String()
}

func chunkRunes(text string, limit int) []string {
	runes := []rune(text)
	chunks := make([]string, 0, len(runes)/limit+1)
	for len(runes) > 0 {
		end := limit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}

func gsm7SeptetLen(text string) int {
	total := 0
	for _, r := range text {
		if _, ok := gsm7RuneToExt[r]; ok {
			total += 2
		} else {
			total++
		}
	}
	return total
}

func chunkGSM7(text string, limit int) []string {
	var chunks []string
	var b strings.Builder
	used := 0
	for _, r := range text {
		size := 1
		if _, ok := gsm7RuneToExt[r]; ok {
			size = 2
		}
		if used > 0 && used+size > limit {
			chunks = append(chunks, b.String())
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += size
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

func concatUDH(ref uint16, part, total int) []byte {
	return []byte{0x05, 0x00, 0x03, byte(ref), byte(total), byte(part)}
}

func randomReference() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint16(b[:])
}

func isGSM7Basic(r rune) bool {
	if r == '\n' || r == '\r' {
		return true
	}
	return (r >= ' ' && r <= '~') || r == '£' || r == '¥' || r == 'è' || r == 'é' || r == 'ù' || r == 'ì' || r == 'ò' || r == 'Ç' || r == 'Ø' || r == 'ø' || r == 'Å' || r == 'å' || r == 'Δ' || r == 'Φ' || r == 'Γ' || r == 'Λ' || r == 'Ω' || r == 'Π' || r == 'Ψ' || r == 'Σ' || r == 'Θ' || r == 'Ξ' || r == 'Æ' || r == 'æ' || r == 'ß' || r == 'É'
}
