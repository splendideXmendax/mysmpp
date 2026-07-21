package dispatch

import "time"

type SourceKind int

const (
	SourceSMPP SourceKind = iota
	SourceHTTPAPI
)

type SubmitSource struct {
	Kind          SourceKind
	SMPPSessionID string
	SMPPSystemID  string
	CallbackURL   string
	CallbackRule  string
}

func (k SourceKind) String() string {
	switch k {
	case SourceSMPP:
		return "smpp"
	case SourceHTTPAPI:
		return "http"
	default:
		return "unknown"
	}
}

type Envelope struct {
	From               string
	To                 string
	Text               string
	ClientID           string
	ClientMsgID        string
	DataCoding         uint8
	Encoding           string
	RegisteredDelivery uint8
	Source             SubmitSource
	ReceivedAt         time.Time
	Meta               map[string]string
	UDH                []byte
	RawPayload         []byte
	RawPayloadSet      bool
	SARRefNum          []byte
	SARTotalSegments   []byte
	SARSegmentSeqnum   []byte
	SARSet             bool
}

type Receipt struct {
	GatewayID  string `json:"gateway_id"`
	ProviderID string `json:"provider_id"`
	Provider   string `json:"provider"`
	Route      string `json:"route"`
	State      string `json:"state"`
}
