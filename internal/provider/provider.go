package provider

import (
	"context"
	"time"
)

type OutboundMessage struct {
	Context            context.Context
	GatewayID          string
	SourceAddr         string
	DestAddr           string
	Text               string
	DataCoding         uint8
	RegisteredDelivery uint8
	Encoding           string
	Meta               map[string]string
	UDH                []byte
	RawPayload         []byte
	RawPayloadSet      bool
	SARRefNum          []byte
	SARTotalSegments   []byte
	SARSegmentSeqnum   []byte
	SARSet             bool
}

type DLR struct {
	Provider   string
	ProviderID string
	State      string
	ErrorCode  int
	DoneAt     time.Time
}

type DLRCallback func(DLR)

// Provider sends MT messages to an upstream. Providers that do not receive DLRs
// directly, such as HTTP adapters using inbound callback rules, may ignore OnDLR.
type Provider interface {
	Send(OutboundMessage) (providerID string, err error)
	OnDLR(DLRCallback)
}

type MultiIDProvider interface {
	Provider
	SendAll(OutboundMessage) (providerIDs []string, err error)
}

type NamedProvider interface {
	Provider
	Name() string
	Close() error
}
