package smppclient

import (
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type Config struct {
	Name     string
	Endpoint string
	SystemID string
	Password string
	SMPP     config.SMPPClientConfig
}

type Message struct {
	GatewayID          string
	SourceAddr         string
	DestAddr           string
	Text               string
	DataCoding         uint8
	RegisteredDelivery uint8
	Encoding           string
	UDH                []byte
	RawPayload         []byte
	RawPayloadSet      bool
	SARRefNum          []byte
	SARTotalSegments   []byte
	SARSegmentSeqnum   []byte
	SARSet             bool
}

type DLR struct {
	ProviderID string
	State      string
	ErrorCode  int
	DoneAt     time.Time
}

type DLRCallback func(DLR)

type PoolStatus struct {
	Name        string
	Endpoint    string
	Connections []ConnectionStatus
}

type ConnectionStatus struct {
	ID             int
	State          string
	Bound          bool
	InFlight       int
	WindowSize     int
	LastInbound    time.Time
	LastError      string
	SubmitOK       uint64
	SubmitFailed   uint64
	DeliverSMCount uint64
}
