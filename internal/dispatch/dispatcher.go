package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type SMPPServer interface {
	Session(id string) (*smpp.Session, bool)
	ReceiversBySystemID(systemID string) []*smpp.Session
}

var errNoReceiverOnline = errors.New("no online receiver for smpp dlr")

type Dispatcher struct {
	logger        *slog.Logger
	registry      *provider.Registry
	router        atomic.Pointer[router.Router]
	store         store.Store
	idAlloc       *idAllocator
	dlrPick       atomic.Uint64
	pendingTTL    time.Duration
	pendingSweep  time.Duration
	claimTimeout  time.Duration
	workers       int
	claimLimit    int
	perWorkerConc int
	pollInterval  time.Duration
	maxAttempts   int
	validateDest  bool
	workerCtx     context.Context
	cancelWorkers context.CancelFunc
	wg            sync.WaitGroup

	mu      sync.RWMutex
	smppSrv SMPPServer
}

func New(logger *slog.Logger, reg *provider.Registry, srv SMPPServer, cfg config.DispatcherConfig, stores ...store.Store) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	if reg == nil {
		reg = provider.NewRegistry()
	}
	var st store.Store
	if len(stores) > 0 {
		st = stores[0]
	}
	if st == nil {
		st = store.NewMemory()
	}
	cfg = normalizeDispatcherConfig(cfg)
	ttl, err := time.ParseDuration(cfg.PendingTTL)
	if err != nil || ttl <= 0 {
		ttl = 30 * time.Minute
	}
	claimTimeout, err := time.ParseDuration(cfg.ClaimTimeout)
	if err != nil || claimTimeout <= 0 {
		claimTimeout = 60 * time.Second
	}
	pendingSweep, err := time.ParseDuration(cfg.PendingSweepInterval)
	if err != nil || pendingSweep <= 0 {
		pendingSweep = time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		logger:        logger,
		registry:      reg,
		smppSrv:       srv,
		store:         st,
		idAlloc:       newIDAllocator(1000, st.ReserveGatewayIDRange),
		pendingTTL:    ttl,
		pendingSweep:  pendingSweep,
		claimTimeout:  claimTimeout,
		workers:       cfg.Workers,
		claimLimit:    cfg.ClaimLimit,
		perWorkerConc: cfg.PerWorkerConcurrency,
		pollInterval:  time.Duration(cfg.PollIntervalMS) * time.Millisecond,
		maxAttempts:   cfg.MaxAttempts,
		validateDest:  cfg.ValidateDestAddrEnabled(),
		workerCtx:     ctx,
		cancelWorkers: cancel,
	}
	reg.SetDLRHandler(d.OnDLR)
	d.StartWorkers(d.workers)
	return d
}

func normalizeDispatcherConfig(cfg config.DispatcherConfig) config.DispatcherConfig {
	if cfg.Workers <= 0 {
		cfg.Workers = 10
	}
	if cfg.PerWorkerConcurrency <= 0 {
		cfg.PerWorkerConcurrency = 10
	}
	if cfg.ClaimLimit <= 0 {
		cfg.ClaimLimit = 20
	}
	if cfg.PollIntervalMS <= 0 {
		cfg.PollIntervalMS = 20
	}
	if cfg.PendingTTL == "" {
		cfg.PendingTTL = "30m"
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.ClaimTimeout == "" {
		cfg.ClaimTimeout = "60s"
	}
	if cfg.PendingSweepInterval == "" {
		cfg.PendingSweepInterval = "1m"
	}
	if cfg.ValidateDestAddr == nil {
		enabled := true
		cfg.ValidateDestAddr = &enabled
	}
	return cfg
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
	env.To = strings.TrimPrefix(env.To, "+")
	rt := d.router.Load()
	if rt == nil {
		return Receipt{}, errors.New("router not initialized")
	}
	route, ok := rt.MatchPhone(env.To)
	if !ok {
		return Receipt{}, fmt.Errorf("no route matched %q", env.To)
	}
	if route.AddrRewrite != (config.AddrRewriteConfig{}) {
		env.To = rewriteDestAddr(env.To, route.AddrRewrite)
	}
	shouldValidate := route.DestAddr.ValidateEnabled(d.validateDest) || route.AddrRewrite.EnforceE164Len
	if shouldValidate {
		opts := destAddrOptions{
			AllowShortCode: route.DestAddr.AllowShortCode,
			MinShortLen:    route.DestAddr.MinShortLen,
			MaxShortLen:    route.DestAddr.MaxShortLen,
		}
		if err := validateDestAddr(env.To, opts); err != nil {
			return Receipt{}, err
		}
	}

	gatewayID, err := d.newGatewayID(ctx)
	if err != nil {
		return Receipt{}, err
	}
	encoding := env.Encoding
	if encoding == "" {
		encoding = message.DetectEncoding(env.Text)
	}
	msg := message.New(gatewayID, message.DirectionMT, env.From, env.To, env.Text)
	msg.Encoding = encoding
	msg.Route = route.Name
	msg.Provider = route.Provider
	msg.SourceKind = env.Source.Kind.String()
	msg.SourceID = env.Source.SMPPSessionID
	msg.Metadata = cloneMeta(env.Meta)
	if env.ClientID != "" {
		if msg.Metadata == nil {
			msg.Metadata = map[string]string{}
		}
		msg.Metadata["client_id"] = env.ClientID
	}
	msg.Segments = message.Split(env.Text, message.SplitOptions{ForceEncoding: encoding})
	msg.State = "queued"
	payload := store.OutboxPayload{
		GatewayID:          gatewayID,
		Provider:           route.Provider,
		Route:              route.Name,
		From:               env.From,
		To:                 env.To,
		Text:               env.Text,
		DataCoding:         env.DataCoding,
		RegisteredDelivery: env.RegisteredDelivery,
		Encoding:           encoding,
		Meta:               cloneMeta(env.Meta),
		SourceKind:         env.Source.Kind.String(),
		SourceSession:      env.Source.SMPPSessionID,
		SourceSystem:       env.Source.SMPPSystemID,
		ReceivedAt:         env.ReceivedAt,
		UDH:                append([]byte(nil), env.UDH...),
	}
	_, existingGatewayID, duplicate, err := d.store.SubmitAtomic(ctx, msg, store.OutboxItem{
		GatewayID:   gatewayID,
		Provider:    route.Provider,
		Payload:     payload,
		MaxAttempts: d.maxAttempts,
	}, env.ClientID, env.ClientMsgID, 24*time.Hour)
	if err != nil {
		return Receipt{}, err
	}
	if duplicate {
		return d.receiptForExisting(ctx, existingGatewayID, route)
	}
	d.logger.Info("message queued", "gateway_id", gatewayID, "provider", route.Provider, "route", route.Name)
	return Receipt{GatewayID: gatewayID, Provider: route.Provider, Route: route.Name, State: "queued"}, nil
}

func (d *Dispatcher) receiptForExisting(ctx context.Context, gatewayID string, route config.RouteConfig) (Receipt, error) {
	msg, found, err := d.store.GetMessage(ctx, gatewayID)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{GatewayID: gatewayID, Provider: route.Provider, Route: route.Name, State: "queued"}
	if found {
		receipt.ProviderID = msg.ProviderID
		receipt.Provider = msg.Provider
		receipt.Route = msg.Route
		receipt.State = msg.State
	}
	return receipt, nil
}

func (d *Dispatcher) OnDLR(dlr provider.DLR) {
	if err := d.HandleDLR(context.Background(), dlr); err != nil {
		d.logger.Warn("dlr rejected", "provider", dlr.Provider, "provider_id", dlr.ProviderID, "err", err)
	}
}

func (d *Dispatcher) HandleDLR(ctx context.Context, dlr provider.DLR) error {
	if ctx == nil {
		ctx = context.Background()
	}
	rec, ok, err := d.store.GetPending(ctx, dlr.ProviderID)
	if err != nil {
		d.logger.Warn("get dlr mapping failed", "provider_id", dlr.ProviderID, "err", err)
		return err
	}
	if !ok {
		d.logger.Warn("dlr mapping not found", "provider_id", dlr.ProviderID)
		return store.ErrNotFound
	}
	if dlr.Provider != "" && rec.Provider != "" && dlr.Provider != rec.Provider {
		return fmt.Errorf("dlr provider mismatch: got %q want %q", dlr.Provider, rec.Provider)
	}
	if dlr.DoneAt.IsZero() {
		dlr.DoneAt = time.Now().UTC()
	}
	if err := d.store.UpdateMessageState(ctx, rec.GatewayID, dlr.State, dlr.ErrorCode); err != nil {
		d.logger.Warn("update dlr state failed", "gateway_id", rec.GatewayID, "err", err)
	}
	if rec.SourceKind == SourceSMPP.String() && rec.RegisteredDelivery&0x03 == 0 {
		_ = d.store.DeletePending(ctx, dlr.ProviderID)
		return nil
	}
	switch rec.SourceKind {
	case SourceSMPP.String():
		if err := d.pushSMPPDLR(rec, dlr); err != nil {
			if errors.Is(err, errNoReceiverOnline) {
				if markErr := d.store.MarkDLRReady(ctx, dlr.ProviderID, dlr.State, dlr.ErrorCode, dlr.DoneAt); markErr != nil {
					d.logger.Warn("mark dlr pending failed", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "err", markErr)
					return markErr
				}
				d.logger.Info("dlr deferred, no receiver online", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "system_id", rec.SourceSystem)
				return nil
			}
			d.logger.Warn("send deliver_sm failed", "gateway_id", rec.GatewayID, "err", err)
			return err
		}
		_ = d.store.DeletePending(ctx, dlr.ProviderID)
	case SourceHTTPAPI.String():
		d.logger.Info("dlr for http source", "gateway_id", rec.GatewayID, "state", dlr.State)
		_ = d.store.DeletePending(ctx, dlr.ProviderID)
	}
	return nil
}

func (d *Dispatcher) PendingSize() int {
	n, _ := d.store.PendingSize(context.Background())
	return n
}

func (d *Dispatcher) Close() error {
	d.cancelWorkers()
	d.wg.Wait()
	return nil
}

func (d *Dispatcher) StartWorkers(n int) {
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("worker-%d", i+1)
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.workerLoop(d.workerCtx, id)
		}()
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.requeueLoop(d.workerCtx)
	}()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.pendingSweepLoop(d.workerCtx)
	}()
}

func (d *Dispatcher) requeueLoop(ctx context.Context) {
	if d.claimTimeout <= 0 {
		return
	}
	interval := d.claimTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	d.requeueStale(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.requeueStale(ctx)
		}
	}
}

func (d *Dispatcher) requeueStale(ctx context.Context) {
	limit := d.claimLimit * d.workers
	if limit <= 0 {
		limit = d.claimLimit
	}
	n, err := d.store.RequeueStaleOutbox(ctx, time.Now().UTC().Add(-d.claimTimeout), limit)
	if err != nil {
		d.logger.Warn("requeue stale outbox failed", "err", err)
		return
	}
	if n > 0 {
		d.logger.Warn("requeued stale outbox", "count", n, "claim_timeout", d.claimTimeout)
	}
}

func (d *Dispatcher) pendingSweepLoop(ctx context.Context) {
	if d.pendingSweep <= 0 {
		return
	}
	ticker := time.NewTicker(d.pendingSweep)
	defer ticker.Stop()
	d.sweepExpiredPending(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sweepExpiredPending(ctx)
		}
	}
}

func (d *Dispatcher) sweepExpiredPending(ctx context.Context) {
	n, err := d.store.SweepExpiredPending(ctx, time.Now().UTC())
	if err != nil {
		d.logger.Warn("sweep expired pending failed", "err", err)
		return
	}
	if n > 0 {
		d.logger.Info("swept expired pending", "count", n)
	}
}

func (d *Dispatcher) workerLoop(ctx context.Context, workerID string) {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	sem := make(chan struct{}, d.perWorkerConc)
	var inFlight sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			inFlight.Wait()
			return
		case <-ticker.C:
			limit := d.claimLimit
			if available := cap(sem) - len(sem); available <= 0 {
				continue
			} else if available < limit {
				limit = available
			}
			items, err := d.store.ClaimOutbox(ctx, workerID, limit)
			if err != nil {
				d.logger.Warn("claim outbox failed", "worker", workerID, "err", err)
				continue
			}
			for _, item := range items {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					inFlight.Wait()
					return
				}
				inFlight.Add(1)
				go func(item store.OutboxItem) {
					defer inFlight.Done()
					defer func() { <-sem }()
					d.processOutbox(ctx, item)
				}(item)
			}
		}
	}
}

func (d *Dispatcher) processOutbox(ctx context.Context, item store.OutboxItem) {
	p, ok := d.registry.Get(item.Provider)
	if !ok {
		d.failOutbox(ctx, item, fmt.Errorf("provider %q not found", item.Provider))
		return
	}
	payload := item.Payload
	providerID, err := p.Send(provider.OutboundMessage{
		Context:            ctx,
		GatewayID:          payload.GatewayID,
		SourceAddr:         payload.From,
		DestAddr:           payload.To,
		Text:               payload.Text,
		DataCoding:         payload.DataCoding,
		RegisteredDelivery: payload.RegisteredDelivery,
		Encoding:           payload.Encoding,
		Meta:               payload.Meta,
		UDH:                append([]byte(nil), payload.UDH...),
	})
	if err != nil {
		d.failOutbox(ctx, item, err)
		return
	}
	if providerID == "" {
		providerID = payload.GatewayID
	}
	if err := d.store.UpdateMessageSent(ctx, payload.GatewayID, providerID); err != nil {
		d.logger.Warn("update sent message failed", "gateway_id", payload.GatewayID, "err", err)
	}
	if err := d.store.SavePending(ctx, store.Pending{
		ProviderID:         providerID,
		GatewayID:          payload.GatewayID,
		SourceKind:         payload.SourceKind,
		SourceSession:      payload.SourceSession,
		SourceSystem:       payload.SourceSystem,
		From:               payload.From,
		To:                 payload.To,
		Text:               payload.Text,
		DataCoding:         payload.DataCoding,
		RegisteredDelivery: payload.RegisteredDelivery,
		Provider:           payload.Provider,
		Route:              payload.Route,
		ReceivedAt:         payload.ReceivedAt,
		ExpiresAt:          time.Now().UTC().Add(d.pendingTTL),
	}); err != nil {
		d.logger.Warn("save pending failed", "gateway_id", payload.GatewayID, "provider_id", providerID, "err", err)
	}
	if err := d.store.AckOutbox(ctx, item.ID); err != nil {
		d.logger.Warn("ack outbox failed", "outbox_id", item.ID, "err", err)
	}
	d.logger.Info("message dispatched", "gateway_id", payload.GatewayID, "provider_id", providerID, "provider", payload.Provider, "route", payload.Route)
}

func (d *Dispatcher) failOutbox(ctx context.Context, item store.OutboxItem, err error) {
	delay := retryDelay(item.Attempt)
	next := time.Now().UTC().Add(delay)
	if item.Attempt >= item.MaxAttempts || isPermanent(err) {
		next = time.Time{}
		_ = d.store.UpdateMessageState(ctx, item.GatewayID, "failed", 1)
	}
	if ferr := d.store.FailOutbox(ctx, item.ID, err.Error(), next); ferr != nil {
		d.logger.Warn("fail outbox failed", "outbox_id", item.ID, "err", ferr)
	}
}

type permanent interface {
	Permanent() bool
}

func isPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p) && p.Permanent()
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
	return base + jitter
}

func (d *Dispatcher) pushSMPPDLR(rec store.Pending, dlr provider.DLR) error {
	d.mu.RLock()
	srv := d.smppSrv
	d.mu.RUnlock()
	if srv == nil {
		return errors.New("dlr has no smpp server")
	}
	var session *smpp.Session
	if rec.SourceSystem != "" {
		receivers := srv.ReceiversBySystemID(rec.SourceSystem)
		if len(receivers) > 0 {
			idx := int(d.dlrPick.Add(1)-1) % len(receivers)
			session = receivers[idx]
		}
	}
	if session == nil && rec.SourceSession != "" {
		if s, ok := srv.Session(rec.SourceSession); ok && s.CanReceive() {
			session = s
		}
	}
	if session == nil {
		return errNoReceiverOnline
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

func (d *Dispatcher) FlushDLR(systemID string) {
	ctx := context.Background()
	items, err := d.store.ListReadyDLR(ctx, systemID, 500)
	if err != nil {
		d.logger.Warn("list pending dlr failed", "system_id", systemID, "err", err)
		return
	}
	for _, rec := range items {
		dlr := provider.DLR{
			Provider:   rec.Provider,
			ProviderID: rec.ProviderID,
			State:      rec.DLRState,
			ErrorCode:  rec.DLRErrorCode,
			DoneAt:     rec.DLRDoneAt,
		}
		if dlr.DoneAt.IsZero() {
			dlr.DoneAt = time.Now().UTC()
		}
		if err := d.pushSMPPDLR(rec, dlr); err != nil {
			if !errors.Is(err, errNoReceiverOnline) {
				d.logger.Warn("flush deliver_sm failed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "err", err)
			}
			return
		}
		_ = d.store.DeletePending(ctx, rec.ProviderID)
		d.logger.Info("dlr flushed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "system_id", rec.SourceSystem)
	}
}

func (d *Dispatcher) newGatewayID(ctx context.Context) (string, error) {
	n, err := d.idAlloc.Next(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("g%012d", n), nil
}

func cloneMeta(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
