package core

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

type Server interface {
	Session(id string) (*smpp.Session, bool)
}

type receiptRecord struct {
	GatewayID          string
	ProviderID         string
	SessionID          string
	ESMESystemID       string
	OriginalFrom       string
	OriginalTo         string
	OriginalText       string
	DataCoding         uint8
	RegisteredDelivery uint8
	SubmittedAt        time.Time
}

type Core struct {
	logger *slog.Logger
	server Server
	prov   provider.Provider
	seq    atomic.Uint64

	mu         sync.RWMutex
	byProvider map[string]receiptRecord
}

func New(logger *slog.Logger) *Core {
	if logger == nil {
		logger = slog.Default()
	}
	return &Core{
		logger:     logger,
		byProvider: map[string]receiptRecord{},
	}
}

func (c *Core) SetServer(server Server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.server = server
}

func (c *Core) SetProvider(prov provider.Provider) {
	c.mu.Lock()
	c.prov = prov
	c.mu.Unlock()
	if prov != nil {
		prov.OnDLR(c.OnDLR)
	}
}

func (c *Core) OnSubmit(session *smpp.Session, submit smpp.SubmitSM) {
	gatewayID := c.newGatewayID()
	submittedAt := time.Now().UTC()
	resp := smpp.PDU{
		CommandID:  smpp.CommandSubmitSMResp,
		Status:     0,
		SequenceID: submit.SequenceID,
		Body:       smpp.CString(gatewayID),
	}

	c.mu.RLock()
	prov := c.prov
	c.mu.RUnlock()
	if prov == nil {
		resp.Status = 0x00000045
		session.Send(resp)
		return
	}

	providerID, err := prov.Send(provider.OutboundMessage{
		GatewayID:          gatewayID,
		SourceAddr:         submit.From,
		DestAddr:           submit.To,
		Text:               submit.Text,
		DataCoding:         submit.DataCoding,
		RegisteredDelivery: submit.RegisteredDelivery,
	})
	if err != nil {
		resp.Status = 0x00000045
		session.Send(resp)
		return
	}

	c.mu.Lock()
	c.byProvider[providerID] = receiptRecord{
		GatewayID:          gatewayID,
		ProviderID:         providerID,
		SessionID:          session.ID(),
		ESMESystemID:       session.SystemID(),
		OriginalFrom:       submit.From,
		OriginalTo:         submit.To,
		OriginalText:       submit.Text,
		DataCoding:         submit.DataCoding,
		RegisteredDelivery: submit.RegisteredDelivery,
		SubmittedAt:        submittedAt,
	}
	c.mu.Unlock()

	session.Send(resp)
	c.logger.Info("submit_sm", "system_id", session.SystemID(), "src", submit.From, "dst", submit.To, "gw_id", gatewayID, "provider_id", providerID)
}

func (c *Core) OnDLR(dlr provider.DLR) {
	c.mu.RLock()
	rec, ok := c.byProvider[dlr.ProviderID]
	server := c.server
	c.mu.RUnlock()
	if !ok {
		c.logger.Warn("dlr mapping not found", "provider_id", dlr.ProviderID)
		return
	}
	if rec.RegisteredDelivery&0x03 == 0 {
		return
	}
	if server == nil {
		c.logger.Warn("dlr has no server", "provider_id", dlr.ProviderID)
		return
	}
	session, ok := server.Session(rec.SessionID)
	if !ok {
		c.logger.Warn("dlr session not found", "session_id", rec.SessionID, "provider_id", dlr.ProviderID)
		return
	}
	if dlr.DoneAt.IsZero() {
		dlr.DoneAt = time.Now().UTC()
	}
	pdu := smpp.BuildDLR(smpp.DLRParams{
		GatewayID:    rec.GatewayID,
		SourceAddr:   rec.OriginalTo,
		DestAddr:     rec.OriginalFrom,
		SubmittedAt:  rec.SubmittedAt,
		DoneAt:       dlr.DoneAt,
		State:        dlr.State,
		ErrorCode:    dlr.ErrorCode,
		OriginalText: rec.OriginalText,
	})
	pdu.SequenceID = session.NextSeq()
	session.Send(pdu)
	c.logger.Info("push dlr", "system_id", rec.ESMESystemID, "gw_id", rec.GatewayID, "state", dlr.State)
}

func (c *Core) newGatewayID() string {
	return fmt.Sprintf("g%010d", c.seq.Add(1))
}
