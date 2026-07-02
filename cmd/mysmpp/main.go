package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/admin"
	"github.com/splendideXmendax/mysmpp/internal/authutil"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/httpgw"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func main() {
	configPath := flag.String("config", "configs/example.json", "path to JSON config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, boot, err := config.LoadStartup(*configPath, os.Getenv("MYSMPP_CONFIG_SEED"))
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}
	if boot.Seeded {
		logger.Info("seeded config", "config", boot.ConfigPath)
	}
	if boot.Generated {
		logger.Info("generated startup credentials", "config", boot.ConfigPath, "credentials", boot.CredentialsPath)
	}
	if strings.EqualFold(cfg.Storage.Driver, "memory") {
		logger.Warn("memory storage is volatile; messages, outbox, pending DLR mappings, and idempotency keys are lost on restart")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.NewFromConfig(cfg)
	if err != nil {
		logger.Error("init store failed", "err", err)
		os.Exit(1)
	}
	if closer, ok := st.(interface{ Close() }); ok {
		defer closer.Close()
	}
	registry := provider.NewRegistry()
	registry.Replace(provider.BuildProviders(ctx, cfg))
	dispatcher := dispatch.New(logger, registry, nil, cfg.Dispatcher, st)
	defer registry.CloseAll()
	dispatcher.ReloadRoutes(cfg.Routes, cfg.Providers)
	httpGateway := httpgw.NewWithDispatcher(cfg, st, dispatcher, registry, ctx, *configPath)
	adminServer := admin.New(httpGateway, *configPath, logger)
	defer adminServer.Close()
	httpGateway.Mount("/admin", http.RedirectHandler("/admin/", http.StatusSeeOther))
	httpGateway.Mount("/admin/", adminServer)

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
		current := httpGateway.Config()
		for _, esme := range current.ESMEs {
			if authutil.ConstantTimeEqual(esme.SystemID, systemID) && authutil.ConstantTimeEqual(esme.Password, password) {
				return true
			}
		}
		return false
	}
	submitJobs := make(chan func(), max(1, cfg.SMPP.MaxSessions*max(1, cfg.SMPP.WindowSize)))
	submitWorkers := max(1, cfg.Dispatcher.Workers*cfg.Dispatcher.PerWorkerConcurrency)
	for i := 0; i < submitWorkers; i++ {
		go func() {
			for job := range submitJobs {
				job()
			}
		}()
	}
	onSubmit := func(session *smpp.Session, submit smpp.SubmitSM) {
		select {
		case submitJobs <- func() {
			defer session.CompleteSubmit()
			receipt, err := dispatcher.Submit(context.Background(), dispatch.Envelope{
				From:               submit.From,
				To:                 submit.To,
				Text:               submit.Text,
				ClientID:           "smpp:" + session.SystemID(),
				ClientMsgID:        smppClientMsgID(session.SystemID(), submit),
				DataCoding:         submit.DataCoding,
				RegisteredDelivery: submit.RegisteredDelivery,
				UDH:                submit.UDH,
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
				if errors.Is(err, dispatch.ErrInvalidDestAddr) {
					resp.Status = smpp.StatusInvalidDestAddr
				} else {
					resp.Status = smpp.StatusSubmitFailed
				}
			} else {
				resp.Body = smpp.CString(receipt.GatewayID)
			}
			session.Send(resp)
		}:
		default:
			session.CompleteSubmit()
			session.Send(smpp.PDU{CommandID: smpp.CommandSubmitSMResp, Status: smpp.StatusThrottled, SequenceID: submit.SequenceID})
		}
	}
	smppServer := smpp.NewServer(cfg.SMPP, logger, auth, onSubmit)
	dispatcher.SetSMPPServer(smppServer)
	smppServer.SetReceiverBoundHandler(dispatcher.FlushDLR)
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
	if err := dispatcher.Close(); err != nil {
		logger.Warn("dispatcher shutdown failed", "err", err)
	}
}

func smppClientMsgID(systemID string, submit smpp.SubmitSM) string {
	h := sha256.New()
	_, _ = h.Write([]byte(systemID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(submit.From))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(submit.To))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(submit.Text))
	_, _ = h.Write([]byte{0, submit.DataCoding})
	_, _ = h.Write(submit.UDH)
	sum := h.Sum(nil)
	return "smpp:" + systemID + ":" + hex.EncodeToString(sum[:12]) + ":" + hex.EncodeToString([]byte{byte(submit.SequenceID >> 24), byte(submit.SequenceID >> 16), byte(submit.SequenceID >> 8), byte(submit.SequenceID)})
}
