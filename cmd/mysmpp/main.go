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
	"github.com/splendideXmendax/mysmpp/internal/httpgw"
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
	httpGateway := httpgw.New(cfg, st)
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

	smppServer := smpp.NewServer(cfg.SMPP, st, logger)
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
