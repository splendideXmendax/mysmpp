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

type Envelope struct {
	From               string
	To                 string
	Text               string
	DataCoding         uint8
	Encoding           string
	RegisteredDelivery uint8
	Source             SubmitSource
	ReceivedAt         time.Time
	Meta               map[string]string
}

type Receipt struct {
	GatewayID  string `json:"gateway_id"`
	ProviderID string `json:"provider_id"`
	Provider   string `json:"provider"`
	Route      string `json:"route"`
	State      string `json:"state"`
}
