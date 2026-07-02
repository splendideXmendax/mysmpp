package provider

import (
	"context"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

type SMPPProvider struct {
	name string
	pool *smppclient.Pool
}

func NewSMPPProvider(ctx context.Context, cfg config.ProviderConfig) *SMPPProvider {
	smppCfg := cfg.SMPP
	if smppCfg == nil {
		defaults := config.DefaultSMPPClientConfig()
		smppCfg = &defaults
	}
	pool := smppclient.NewPool(ctx, smppclient.Config{
		Name:     cfg.Name,
		Endpoint: cfg.Endpoint,
		SystemID: cfg.SystemID,
		Password: cfg.Password,
		SMPP:     *smppCfg,
	})
	return &SMPPProvider{name: cfg.Name, pool: pool}
}

func (p *SMPPProvider) Name() string { return p.name }

func (p *SMPPProvider) Send(msg OutboundMessage) (string, error) {
	ids, err := p.SendAll(msg)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

func (p *SMPPProvider) SendAll(msg OutboundMessage) ([]string, error) {
	return p.pool.SendAll(msg.Context, smppclient.Message{
		GatewayID:          msg.GatewayID,
		SourceAddr:         msg.SourceAddr,
		DestAddr:           msg.DestAddr,
		Text:               msg.Text,
		DataCoding:         msg.DataCoding,
		RegisteredDelivery: msg.RegisteredDelivery,
		Encoding:           msg.Encoding,
		UDH:                append([]byte(nil), msg.UDH...),
	})
}

func (p *SMPPProvider) OnDLR(cb DLRCallback) {
	if cb == nil {
		p.pool.OnDLR(nil)
		return
	}
	p.pool.OnDLR(func(dlr smppclient.DLR) {
		cb(DLR{
			Provider:   p.name,
			ProviderID: dlr.ProviderID,
			State:      dlr.State,
			ErrorCode:  dlr.ErrorCode,
			DoneAt:     dlr.DoneAt,
		})
	})
}

func (p *SMPPProvider) Close() error {
	return p.pool.Close()
}

func (p *SMPPProvider) Status() smppclient.PoolStatus {
	return p.pool.Status()
}

func (p *SMPPProvider) SMPPStatus() (smppclient.PoolStatus, bool) {
	return p.Status(), true
}
