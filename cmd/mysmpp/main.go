package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/httpgw"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to JSON config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := store.NewMemory()
	registry := provider.NewRegistry()
	registry.Replace(buildProviders(ctx, cfg))
	dispatcher := dispatch.New(logger, registry, nil, 30*time.Minute)
	defer dispatcher.Close()
	defer registry.CloseAll()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)
	httpGateway := httpgw.NewWithDispatcher(cfg, st, dispatcher)

	httpServer := &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           httpGateway.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("http listening", "addr", cfg.Server.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	auth := func(systemID, password string) bool {
		for _, esme := range cfg.ESMEs {
			if esme.SystemID == systemID && esme.Password == password {
				return true
			}
		}
		return cfg.SMPP.SystemID != "" && systemID == cfg.SMPP.SystemID && password == cfg.SMPP.Password
	}
	onSubmit := func(session *smpp.Session, submit smpp.SubmitSM) {
		receipt, err := dispatcher.Submit(context.Background(), dispatch.Envelope{
			From:               submit.From,
			To:                 submit.To,
			Text:               submit.Text,
			DataCoding:         submit.DataCoding,
			RegisteredDelivery: submit.RegisteredDelivery,
			ReceivedAt:         time.Now().UTC(),
			Source: dispatch.SubmitSource{
				Kind:          dispatch.SourceSMPP,
				SMPPSessionID: session.ID(),
				SMPPSystemID:  session.SystemID(),
			},
		})
		resp := smpp.PDU{
			CommandID:  smpp.CommandSubmitSMResp,
			SequenceID: submit.SequenceID,
		}
		if err != nil {
			resp.Status = 0x00000045
		} else {
			resp.Body = smpp.CString(receipt.GatewayID)
		}
		session.Send(resp)
	}
	smppServer := smpp.NewServer(cfg.SMPP, logger, auth, onSubmit)
	dispatcher.SetSMPPServer(smppServer)
	go func() {
		if err := smppServer.ListenAndServe(ctx); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		logger.Error("server failed", "err", err)
		stop()
	}

	timeout := 10 * time.Second
	if cfg.Server.ShutdownTimeout != "" {
		if parsed, err := time.ParseDuration(cfg.Server.ShutdownTimeout); err == nil {
			timeout = parsed
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown failed", "err", err)
	}
}

func buildProviders(ctx context.Context, cfg config.Config) map[string]provider.Provider {
	ruleByName := map[string]config.HTTPRuleConfig{}
	for _, rule := range cfg.Outbound {
		ruleByName[rule.Name] = rule
	}
	out := map[string]provider.Provider{}
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		switch p.Protocol {
		case "http", "https":
			rule, ok := ruleByName[p.Rule]
			if !ok {
				continue
			}
			out[p.Name] = provider.NewHTTPProvider(p, rule)
		case "mock":
			mock := provider.NewNamedMock(ctx, p.Name)
			mock.DelayMin = 2 * time.Second
			mock.DelayMax = 4 * time.Second
			out[p.Name] = mock
		}
	}
	if len(out) == 0 {
		mock := provider.NewNamedMock(ctx, "mock")
		mock.DelayMin = 2 * time.Second
		mock.DelayMax = 4 * time.Second
		out["mock"] = mock
	}
	return out
}
