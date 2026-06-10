package smppclient

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

var (
	receiptIDRe     = regexp.MustCompile(`(?i)\bid:\s*([^\s]+)`)
	receiptStatRe   = regexp.MustCompile(`(?i)\bstat:\s*([A-Za-z]+)`)
	receiptErrRe    = regexp.MustCompile(`(?i)\berr:\s*(\d+)`)
	receiptDoneRe   = regexp.MustCompile(`(?i)\bdone\s+date:\s*(\d{10,12})`)
	stateByTLVValue = map[byte]string{
		1: "ENROUTE",
		2: "DELIVRD",
		3: "EXPIRED",
		4: "DELETED",
		5: "UNDELIV",
		6: "ACCEPTD",
		7: "UNKNOWN",
		8: "REJECTD",
	}
)

func ParseDeliverSM(body []byte, idSource, idFormat string) (DLR, bool, bool) {
	parsed, tlvs, ok := parseDeliverBody(body)
	if !ok {
		return DLR{}, false, false
	}
	if parsed.esmClass&0x04 == 0 {
		return DLR{}, false, false
	}
	text := parsed.text
	textID := firstSubmatch(receiptIDRe, text)
	tlvID := ""
	if value, ok := smpp.FindTLV(tlvs, smpp.TagReceiptedMessageID); ok {
		tlvID = strings.TrimRight(string(value), "\x00")
	}
	id := textID
	switch strings.ToLower(idSource) {
	case "tlv":
		id = tlvID
	case "text":
		id = textID
	default:
		if tlvID != "" {
			id = tlvID
		}
	}
	if id == "" {
		return DLR{}, false, true
	}

	state := strings.ToUpper(firstSubmatch(receiptStatRe, text))
	if value, ok := smpp.FindTLV(tlvs, smpp.TagMessageState); ok && len(value) > 0 {
		if mapped := stateByTLVValue[value[0]]; mapped != "" {
			state = mapped
		}
	}
	if state == "" {
		state = "UNKNOWN"
	}
	errCode, _ := strconv.Atoi(firstSubmatch(receiptErrRe, text))
	doneAt := parseDoneAt(firstSubmatch(receiptDoneRe, text))
	if doneAt.IsZero() {
		doneAt = time.Now().UTC()
	}
	return DLR{ProviderID: NormalizeID(id, idFormat), State: state, ErrorCode: errCode, DoneAt: doneAt}, true, true
}

type deliverBody struct {
	esmClass   byte
	dataCoding byte
	text       string
}

func parseDeliverBody(body []byte) (deliverBody, []smpp.TLV, bool) {
	offset := 0
	if !skipCString(body, &offset) || offset+2 > len(body) {
		return deliverBody{}, nil, false
	}
	offset += 2
	if !skipCString(body, &offset) || offset+2 > len(body) {
		return deliverBody{}, nil, false
	}
	offset += 2
	if !skipCString(body, &offset) || offset+3 > len(body) {
		return deliverBody{}, nil, false
	}
	esmClass := body[offset]
	offset += 3
	if !skipCString(body, &offset) || !skipCString(body, &offset) || offset+5 > len(body) {
		return deliverBody{}, nil, false
	}
	offset += 2
	dataCoding := body[offset]
	offset += 2
	if offset >= len(body) {
		return deliverBody{}, nil, false
	}
	smLen := int(body[offset])
	offset++
	if offset+smLen > len(body) {
		return deliverBody{}, nil, false
	}
	raw := body[offset : offset+smLen]
	offset += smLen
	tlvs := smpp.ParseTLVs(body[offset:])
	return deliverBody{esmClass: esmClass, dataCoding: dataCoding, text: message.DecodeSubmitText(raw, dataCoding)}, tlvs, true
}

func skipCString(body []byte, offset *int) bool {
	for *offset < len(body) && body[*offset] != 0x00 {
		(*offset)++
	}
	if *offset >= len(body) {
		return false
	}
	(*offset)++
	return true
}

func firstSubmatch(re *regexp.Regexp, text string) string {
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func parseDoneAt(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	layouts := []string{"0601021504", "060102150405"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
