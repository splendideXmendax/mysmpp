package provider

import "time"

type OutboundMessage struct {
	GatewayID          string
	SourceAddr         string
	DestAddr           string
	Text               string
	DataCoding         uint8
	RegisteredDelivery uint8
}

type DLR struct {
	ProviderID string
	State      string
	ErrorCode  int
	DoneAt     time.Time
}

type DLRCallback func(DLR)

type Provider interface {
	Send(OutboundMessage) (providerID string, err error)
	OnDLR(DLRCallback)
}
