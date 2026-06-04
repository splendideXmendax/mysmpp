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

type NamedProvider interface {
	Provider
	Name() string
	Close() error
}
