package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

type SMPPServer interface {
	Session(id string) (*smpp.Session, bool)
}

type Dispatcher struct {
	logger   *slog.Logger
	registry *provider.Registry
	router   atomic.Pointer[router.Router]
	pending  *pendingMap
	seq      atomic.Uint64

	mu      sync.RWMutex
	smppSrv SMPPServer
}

func New(logger *slog.Logger, reg *provider.Registry, srv SMPPServer, ttl time.Duration) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	if reg == nil {
		reg = provider.NewRegistry()
	}
	d := &Dispatcher{
		logger:   logger,
		registry: reg,
		smppSrv:  srv,
		pending:  newPendingMap(ttl),
	}
	reg.SetDLRHandler(d.OnDLR)
	return d
}

func (d *Dispatcher) SetSMPPServer(srv SMPPServer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.smppSrv = srv
}

func (d *Dispatcher) ReloadRoutes(routes []config.RouteConfig, providers []config.ProviderConfig) {
	d.router.Store(router.NewWithProviders(routes, providers))
}

func (d *Dispatcher) Submit(ctx context.Context, env Envelope) (Receipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if env.ReceivedAt.IsZero() {
		env.ReceivedAt = time.Now().UTC()
	}
	rt := d.router.Load()
	if rt == nil {
		return Receipt{}, errors.New("router not initialized")
	}
	route, ok := rt.MatchPhone(env.To)
	if !ok {
		return Receipt{}, fmt.Errorf("no route matched %q", env.To)
	}
	p, ok := d.registry.Get(route.Provider)
	if !ok {
		return Receipt{}, fmt.Errorf("provider %q not found", route.Provider)
	}

	gatewayID := d.newGatewayID()
	providerID, err := p.Send(provider.OutboundMessage{
		Context:            ctx,
		GatewayID:          gatewayID,
		SourceAddr:         env.From,
		DestAddr:           env.To,
		Text:               env.Text,
		DataCoding:         env.DataCoding,
		RegisteredDelivery: env.RegisteredDelivery,
		Encoding:           env.Encoding,
		Meta:               env.Meta,
	})
	if err != nil {
		return Receipt{}, err
	}
	if providerID == "" {
		providerID = gatewayID
	}

	d.pending.Put(providerID, pendingRecord{
		GatewayID:          gatewayID,
		Source:             env.Source,
		ReceivedAt:         env.ReceivedAt,
		From:               env.From,
		To:                 env.To,
		Text:               env.Text,
		DataCoding:         env.DataCoding,
		RegisteredDelivery: env.RegisteredDelivery,
		Provider:           route.Provider,
		Route:              route.Name,
	})
	d.logger.Info("message dispatched", "gw_id", gatewayID, "provider_id", providerID, "provider", route.Provider, "route", route.Name)
	return Receipt{GatewayID: gatewayID, ProviderID: providerID, Provider: route.Provider, Route: route.Name, State: "submitted"}, nil
}

func (d *Dispatcher) OnDLR(dlr provider.DLR) {
	rec, ok := d.pending.Get(dlr.ProviderID)
	if !ok {
		d.logger.Warn("dlr mapping not found", "provider_id", dlr.ProviderID)
		return
	}
	if dlr.DoneAt.IsZero() {
		dlr.DoneAt = time.Now().UTC()
	}
	if rec.Source.Kind == SourceSMPP && rec.RegisteredDelivery&0x03 == 0 {
		d.pending.Complete(dlr.ProviderID)
		return
	}
	switch rec.Source.Kind {
	case SourceSMPP:
		if err := d.pushSMPPDLR(rec, dlr); err != nil {
			d.logger.Warn("send deliver_sm failed", "gw_id", rec.GatewayID, "err", err)
			return
		}
		d.pending.Complete(dlr.ProviderID)
	case SourceHTTPAPI:
		d.logger.Info("dlr for http source", "gw_id", rec.GatewayID, "state", dlr.State)
		d.pending.Complete(dlr.ProviderID)
	}
}

func (d *Dispatcher) PendingSize() int { return d.pending.Size() }

func (d *Dispatcher) Close() error {
	d.pending.Close()
	return nil
}

func (d *Dispatcher) pushSMPPDLR(rec pendingRecord, dlr provider.DLR) error {
	d.mu.RLock()
	srv := d.smppSrv
	d.mu.RUnlock()
	if srv == nil {
		return errors.New("dlr has no smpp server")
	}
	session, ok := srv.Session(rec.Source.SMPPSessionID)
	if !ok {
		return fmt.Errorf("dlr session %q not found", rec.Source.SMPPSessionID)
	}
	pdu := smpp.BuildDLR(smpp.DLRParams{
		GatewayID:    rec.GatewayID,
		SourceAddr:   rec.To,
		DestAddr:     rec.From,
		SubmittedAt:  rec.ReceivedAt,
		DoneAt:       dlr.DoneAt,
		State:        dlr.State,
		ErrorCode:    dlr.ErrorCode,
		OriginalText: rec.Text,
	})
	pdu.SequenceID = session.NextSeq()
	session.Send(pdu)
	return nil
}

func (d *Dispatcher) newGatewayID() string {
	return fmt.Sprintf("g%010d", d.seq.Add(1))
}
