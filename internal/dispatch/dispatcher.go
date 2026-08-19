package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/cdr"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/filter"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
	"github.com/splendideXmendax/mysmpp/internal/tenant"
)

type SMPPServer interface {
	Session(id string) (*smpp.Session, bool)
	ReceiversBySystemID(systemID string) []*smpp.Session
}

type CDRSink interface {
	Emit(cdr.Event)
	Close() error
}

var (
	errNoReceiverOnline = errors.New("no online receiver for smpp dlr")
	errDLRNotAcked      = errors.New("smpp dlr was not acknowledged")
)

type Dispatcher struct {
	logger         *slog.Logger
	registry       *provider.Registry
	router         atomic.Pointer[router.Router]
	filterEngine   atomic.Pointer[filter.Engine]
	cdrSink        atomic.Pointer[cdrSinkHolder]
	store          store.Store
	idAlloc        *idAllocator
	dlrPick        atomic.Uint64
	pendingTTL     time.Duration
	pendingSweep   time.Duration
	claimTimeout   time.Duration
	workers        int
	claimLimit     int
	perWorkerConc  int
	pollInterval   time.Duration
	maxAttempts    int
	validateDest   bool
	dlrLookupWait  time.Duration
	workerCtx      context.Context
	cancelWorkers  context.CancelFunc
	wg             sync.WaitGroup
	httpClient     *http.Client
	dlrCh          chan provider.DLR
	dlrLocks       [256]sync.Mutex
	instanceID     atomic.Pointer[string]
	tenantResolver atomic.Pointer[tenantResolverHolder]
	rateLimiter    tenant.RateLimiter

	mu      sync.RWMutex
	smppSrv SMPPServer
}

type cdrSinkHolder struct {
	sink CDRSink
}

type tenantResolverHolder struct {
	resolver tenant.Resolver
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
		dlrLookupWait: 2 * time.Second,
		workerCtx:     ctx,
		cancelWorkers: cancel,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
		dlrCh:         make(chan provider.DLR, 4096),
		rateLimiter:   tenant.NewTokenBucket(),
	}
	d.tenantResolver.Store(&tenantResolverHolder{resolver: tenant.NewResolver(config.Config{})})
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

func (d *Dispatcher) SetFilterEngine(e *filter.Engine) {
	d.filterEngine.Store(e)
}

func (d *Dispatcher) ReloadFilter(e *filter.Engine) {
	d.SetFilterEngine(e)
}

func (d *Dispatcher) ReloadTenants(cfg config.Config) {
	d.tenantResolver.Store(&tenantResolverHolder{resolver: tenant.NewResolver(cfg)})
}

func (d *Dispatcher) SetCDRSink(s CDRSink) {
	if s == nil {
		d.cdrSink.Store(nil)
		return
	}
	d.cdrSink.Store(&cdrSinkHolder{sink: s})
}

func (d *Dispatcher) SetInstanceID(instance string) {
	d.instanceID.Store(&instance)
}

func (d *Dispatcher) Submit(ctx context.Context, env Envelope) (Receipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if env.ReceivedAt.IsZero() {
		env.ReceivedAt = time.Now().UTC()
	}
	identity, hasIdentity := d.resolveTenant(env)
	if hasIdentity {
		env.TenantID = identity.TenantID
		env.AccountID = identity.AccountID
		if !identity.Enabled {
			d.emitSubmitRejected(env, "tenant_disabled")
			return Receipt{}, ErrTenantDisabled
		}
		if !d.rateLimiter.Allow(identity.TenantID, identity.TPS, identity.Burst) {
			if env.ClientID != "" && env.ClientMsgID != "" {
				gatewayID, found, err := d.store.CheckIdempotency(ctx, env.ClientID, env.ClientMsgID)
				if err != nil {
					return Receipt{}, err
				}
				if found {
					return d.receiptForExisting(ctx, gatewayID)
				}
			}
			d.emitSubmitRejected(env, "tenant_tps")
			return Receipt{}, ErrRateExceeded
		}
	}
	env.To = strings.TrimPrefix(env.To, "+")
	filterDecision := filter.Decision{Action: filter.ActionPass, NewText: env.Text}
	if engine := d.filterEngine.Load(); engine != nil {
		filterDecision = engine.Evaluate(env.Text)
		if filterDecision.Action == filter.ActionBlock {
			d.emitCDR(cdr.Event{
				Kind:        "rejected",
				From:        env.From,
				To:          env.To,
				TextLen:     len([]rune(env.Text)),
				TextHash:    cdr.TextHash(env.Text),
				ClientID:    env.ClientID,
				TenantID:    env.TenantID,
				AccountID:   env.AccountID,
				ClientMsgID: env.ClientMsgID,
				SystemID:    env.Source.SMPPSystemID,
				Source:      env.Source.Kind.String(),
				Reason:      "filter_block",
				FilterRule:  filterDecision.Reason,
			})
			return Receipt{}, fmt.Errorf("%w: %s", ErrBlocked, filterDecision.Reason)
		}
		if filterDecision.Action == filter.ActionMask {
			if len(env.UDH) > 0 || env.SARSet {
				d.emitCDR(cdr.Event{
					Kind:        "rejected",
					From:        env.From,
					To:          env.To,
					TextLen:     len([]rune(env.Text)),
					TextHash:    cdr.TextHash(env.Text),
					ClientID:    env.ClientID,
					TenantID:    env.TenantID,
					AccountID:   env.AccountID,
					ClientMsgID: env.ClientMsgID,
					SystemID:    env.Source.SMPPSystemID,
					Source:      env.Source.Kind.String(),
					Reason:      "filter_mask_multipart",
					FilterRule:  filterDecision.Reason,
				})
				return Receipt{}, fmt.Errorf("%w: cannot mask an individual multipart segment", ErrBlocked)
			}
			env.Text = filterDecision.NewText
			env.RawPayload = nil
			env.RawPayloadSet = false
			env.Encoding = ""
			env.DataCoding = 0
		}
	}
	rt := d.router.Load()
	if rt == nil {
		return Receipt{}, errors.New("router not initialized")
	}
	matchInput := router.MatchInput{
		To:       env.To,
		From:     env.From,
		SystemID: env.Source.SMPPSystemID,
		ClientID: env.ClientID,
		Tags:     filterDecision.Tags,
		Now:      env.ReceivedAt,
	}
	route, ok := rt.MatchRoute(matchInput)
	if !ok {
		d.emitCDR(cdr.Event{
			Kind:        "rejected",
			From:        env.From,
			To:          env.To,
			TextLen:     len([]rune(env.Text)),
			TextHash:    cdr.TextHash(env.Text),
			ClientID:    env.ClientID,
			TenantID:    env.TenantID,
			AccountID:   env.AccountID,
			ClientMsgID: env.ClientMsgID,
			SystemID:    env.Source.SMPPSystemID,
			Source:      env.Source.Kind.String(),
			Reason:      "no_route",
		})
		return Receipt{}, fmt.Errorf("%w %q", ErrNoRoute, env.To)
	}
	providerHint := ""
	if hint, found := rt.SelectProvider(route, ""); found {
		providerHint = hint.Provider
	}
	if route.AddrRewrite != (config.AddrRewriteConfig{}) {
		env.To = rewriteDestAddr(env.To, route.AddrRewrite)
	}
	shouldValidate := route.DestAddr.ValidateEnabled(d.validateDest) || route.AddrRewrite.EnforceE164Len
	if shouldValidate {
		opts := destAddrOptions{
			AllowShortCode:    route.DestAddr.AllowShortCode,
			MinShortLen:       route.DestAddr.MinShortLen,
			MaxShortLen:       route.DestAddr.MaxShortLen,
			CountryLengthMode: route.DestAddr.CountryLengthMode,
		}
		if err := validateDestAddr(env.To, opts); err != nil {
			d.emitCDR(cdr.Event{
				Kind:        "rejected",
				From:        env.From,
				To:          env.To,
				TextLen:     len([]rune(env.Text)),
				TextHash:    cdr.TextHash(env.Text),
				ClientID:    env.ClientID,
				TenantID:    env.TenantID,
				AccountID:   env.AccountID,
				ClientMsgID: env.ClientMsgID,
				SystemID:    env.Source.SMPPSystemID,
				Source:      env.Source.Kind.String(),
				Route:       route.Name,
				Provider:    providerHint,
				Reason:      "bad_dest",
			})
			return Receipt{}, err
		}
	}

	gatewayID, err := d.newGatewayID(ctx)
	if err != nil {
		return Receipt{}, err
	}
	match, ok := rt.SelectProvider(route, gatewayID)
	if !ok {
		d.emitCDR(cdr.Event{
			Kind:        "rejected",
			GatewayID:   gatewayID,
			From:        env.From,
			To:          env.To,
			TextLen:     len([]rune(env.Text)),
			TextHash:    cdr.TextHash(env.Text),
			ClientID:    env.ClientID,
			TenantID:    env.TenantID,
			AccountID:   env.AccountID,
			ClientMsgID: env.ClientMsgID,
			SystemID:    env.Source.SMPPSystemID,
			Source:      env.Source.Kind.String(),
			Route:       route.Name,
			Reason:      "no_provider",
		})
		return Receipt{}, fmt.Errorf("%w for route %q", ErrNoRoute, route.Name)
	}
	encoding := env.Encoding
	if encoding == "" {
		encoding = message.DetectEncoding(env.Text)
	}
	msg := message.New(gatewayID, message.DirectionMT, env.From, env.To, env.Text)
	msg.Encoding = encoding
	msg.Route = route.Name
	msg.Provider = match.Provider
	msg.SourceKind = env.Source.Kind.String()
	msg.SourceID = env.Source.SMPPSessionID
	msg.TenantID = env.TenantID
	msg.AccountID = env.AccountID
	msg.ClientMsgID = env.ClientMsgID
	msg.Metadata = cloneMeta(env.Meta)
	if env.ClientID != "" {
		if msg.Metadata == nil {
			msg.Metadata = map[string]string{}
		}
		msg.Metadata["client_id"] = env.ClientID
	}
	msg.Segments = message.Split(env.Text, message.SplitOptions{ForceEncoding: encoding})
	msg.State = "queued"
	payloadMeta := cloneMeta(env.Meta)
	if env.ClientID != "" {
		if payloadMeta == nil {
			payloadMeta = map[string]string{}
		}
		payloadMeta["client_id"] = env.ClientID
	}
	payload := store.OutboxPayload{
		GatewayID:          gatewayID,
		TenantID:           env.TenantID,
		AccountID:          env.AccountID,
		ClientMsgID:        env.ClientMsgID,
		Provider:           match.Provider,
		Route:              route.Name,
		From:               env.From,
		To:                 env.To,
		Text:               env.Text,
		DataCoding:         env.DataCoding,
		RegisteredDelivery: env.RegisteredDelivery,
		Encoding:           encoding,
		Meta:               payloadMeta,
		SourceKind:         env.Source.Kind.String(),
		SourceSession:      env.Source.SMPPSessionID,
		SourceSystem:       env.Source.SMPPSystemID,
		CallbackURL:        env.Source.CallbackURL,
		CallbackRule:       env.Source.CallbackRule,
		ReceivedAt:         env.ReceivedAt,
		UDH:                append([]byte(nil), env.UDH...),
		RawPayload:         append([]byte(nil), env.RawPayload...),
		RawPayloadSet:      env.RawPayloadSet,
		SARRefNum:          append([]byte(nil), env.SARRefNum...),
		SARTotalSegments:   append([]byte(nil), env.SARTotalSegments...),
		SARSegmentSeqnum:   append([]byte(nil), env.SARSegmentSeqnum...),
		SARSet:             env.SARSet,
	}
	submitOpts := store.SubmitOptions{Idempotency: store.IdempotencyOptions{
		ClientID: env.ClientID,
		Key:      env.ClientMsgID,
		TTL:      24 * time.Hour,
	}}
	if hasIdentity && identity.DailySegments > 0 {
		location := identity.Location
		if location == nil {
			location = time.UTC
		}
		submitOpts.Quota = &store.DailyQuotaDebit{
			TenantID: identity.TenantID,
			Date:     time.Now().In(location).Format(time.DateOnly),
			Segments: len(msg.Segments),
			Limit:    identity.DailySegments,
		}
	}
	_, existingGatewayID, duplicate, err := d.store.SubmitAtomic(ctx, msg, store.OutboxItem{
		GatewayID:   gatewayID,
		Provider:    match.Provider,
		Payload:     payload,
		MaxAttempts: d.maxAttempts,
	}, submitOpts)
	if err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			d.emitSubmitRejected(env, "tenant_daily_segments")
		}
		return Receipt{}, err
	}
	if duplicate {
		return d.receiptForExisting(ctx, existingGatewayID)
	}
	d.emitCDR(cdr.Event{
		Kind:        "accepted",
		GatewayID:   gatewayID,
		From:        env.From,
		To:          env.To,
		TextLen:     len([]rune(env.Text)),
		TextHash:    cdr.TextHash(env.Text),
		Encoding:    encoding,
		Segments:    len(msg.Segments),
		Route:       route.Name,
		Provider:    match.Provider,
		ClientID:    env.ClientID,
		TenantID:    env.TenantID,
		AccountID:   env.AccountID,
		ClientMsgID: env.ClientMsgID,
		SystemID:    env.Source.SMPPSystemID,
		Source:      env.Source.Kind.String(),
		State:       "queued",
	})
	d.logger.Info("message queued", "gateway_id", gatewayID, "provider", match.Provider, "route", route.Name, "source", env.Source.Kind.String(), "source_session", env.Source.SMPPSessionID, "system_id", env.Source.SMPPSystemID, "registered_delivery", env.RegisteredDelivery)
	return Receipt{GatewayID: gatewayID, Provider: match.Provider, Route: route.Name, State: "queued"}, nil
}

func (d *Dispatcher) receiptForExisting(ctx context.Context, gatewayID string) (Receipt, error) {
	msg, found, err := d.store.GetMessage(ctx, gatewayID)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{GatewayID: gatewayID, State: "queued"}
	if found {
		receipt.ProviderID = msg.ProviderID
		receipt.Provider = msg.Provider
		receipt.Route = msg.Route
		receipt.State = msg.State
	}
	return receipt, nil
}

func (d *Dispatcher) resolveTenant(env Envelope) (tenant.Identity, bool) {
	holder := d.tenantResolver.Load()
	if holder == nil || holder.resolver == nil {
		return tenant.Identity{}, false
	}
	switch env.Source.Kind {
	case SourceSMPP:
		return holder.resolver.Resolve(tenant.ProtocolSMPP, env.Source.SMPPSystemID)
	case SourceHTTPAPI:
		return holder.resolver.Resolve(tenant.ProtocolHTTP, env.ClientID)
	default:
		return tenant.Identity{}, false
	}
}

func (d *Dispatcher) emitSubmitRejected(env Envelope, reason string) {
	d.emitCDR(cdr.Event{
		Kind:        "rejected",
		From:        env.From,
		To:          env.To,
		TextLen:     len([]rune(env.Text)),
		TextHash:    cdr.TextHash(env.Text),
		ClientID:    env.ClientID,
		TenantID:    env.TenantID,
		AccountID:   env.AccountID,
		ClientMsgID: env.ClientMsgID,
		SystemID:    env.Source.SMPPSystemID,
		Source:      env.Source.Kind.String(),
		Reason:      reason,
	})
}

func (d *Dispatcher) OnDLR(dlr provider.DLR) {
	select {
	case d.dlrCh <- dlr:
	default:
		d.logger.Warn("dlr channel full, handling in background", "provider", dlr.Provider, "provider_id", dlr.ProviderID)
		go func() {
			if err := d.HandleDLR(context.Background(), dlr); err != nil {
				d.logger.Warn("dlr rejected", "provider", dlr.Provider, "provider_id", dlr.ProviderID, "err", err)
			}
		}()
	}
}

func (d *Dispatcher) HandleDLR(ctx context.Context, dlr provider.DLR) error {
	if ctx == nil {
		ctx = context.Background()
	}
	unlock := d.lockDLR(dlr.Provider, dlr.ProviderID)
	defer unlock()
	rec, ok, err := d.getPendingForDLR(ctx, dlr.Provider, dlr.ProviderID)
	if err != nil {
		d.logger.Warn("get dlr mapping failed", "provider", dlr.Provider, "provider_id", dlr.ProviderID, "err", err)
		return err
	}
	if !ok {
		d.logger.Warn("dlr mapping not found", "provider", dlr.Provider, "provider_id", dlr.ProviderID)
		return store.ErrNotFound
	}
	if dlr.DoneAt.IsZero() {
		dlr.DoneAt = time.Now().UTC()
	}
	if err := d.store.UpdatePendingDLR(ctx, rec.Provider, rec.ProviderID, dlr.State, dlr.ErrorCode, dlr.DoneAt); err != nil {
		return err
	}
	rec.DLRReady = false
	rec.DLRDelivered = false
	rec.DLRState = dlr.State
	rec.DLRErrorCode = dlr.ErrorCode
	rec.DLRDoneAt = dlr.DoneAt
	segments, err := d.store.ListPendingByGatewayID(ctx, rec.GatewayID)
	if err != nil {
		return err
	}
	aggregate := aggregateDLR(segments)
	if aggregate.Final {
		if err := d.store.UpdateMessageState(ctx, rec.GatewayID, aggregate.State, aggregate.ErrorCode); err != nil {
			d.logger.Warn("update aggregate dlr state failed", "gateway_id", rec.GatewayID, "err", err)
		}
	}
	d.emitCDR(d.dlrEvent(ctx, rec, dlr, aggregate))
	if rec.SourceKind == SourceSMPP.String() && rec.RegisteredDelivery&0x03 == 0 {
		d.logger.Info("dlr skipped, registered_delivery not requested", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "registered_delivery", rec.RegisteredDelivery, "source_session", rec.SourceSession, "system_id", rec.SourceSystem)
		return d.finishDLRDelivery(ctx, rec)
	}
	switch rec.SourceKind {
	case SourceSMPP.String():
		if err := d.pushSMPPDLR(rec, dlr); err != nil {
			if errors.Is(err, errNoReceiverOnline) || errors.Is(err, errDLRNotAcked) {
				if markErr := d.store.MarkDLRReady(ctx, rec.Provider, rec.ProviderID, dlr.State, dlr.ErrorCode, dlr.DoneAt); markErr != nil {
					d.logger.Warn("mark dlr pending failed", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "err", markErr)
					return markErr
				}
				d.logger.Info("dlr deferred", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "system_id", rec.SourceSystem, "reason", err)
				return nil
			}
			d.logger.Warn("send deliver_sm failed", "gateway_id", rec.GatewayID, "err", err)
			return err
		}
		return d.finishDLRDelivery(ctx, rec)
	case SourceHTTPAPI.String():
		if rec.CallbackURL != "" {
			if err := d.sendHTTPCallback(ctx, rec, dlr, aggregate); err != nil {
				d.logger.Warn("http dlr callback failed", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "url", rec.CallbackURL, "err", err)
				return err
			}
			d.logger.Info("http dlr callback sent", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "url", rec.CallbackURL)
		} else {
			d.logger.Info("dlr for http source without callback", "gateway_id", rec.GatewayID, "state", dlr.State)
		}
		return d.finishDLRDelivery(ctx, rec)
	}
	return nil
}

func (d *Dispatcher) getPendingForDLR(ctx context.Context, provider, providerID string) (store.Pending, bool, error) {
	deadline := time.Now().Add(d.dlrLookupWait)
	for {
		rec, ok, err := d.store.GetPending(ctx, provider, providerID)
		if err != nil || ok || d.dlrLookupWait <= 0 || time.Now().After(deadline) {
			return rec, ok, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return store.Pending{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) PendingSize() int {
	n, _ := d.store.PendingSize(context.Background())
	return n
}

func (d *Dispatcher) Close() error {
	d.cancelWorkers()
	d.wg.Wait()
	if holder := d.cdrSink.Load(); holder != nil && holder.sink != nil {
		return holder.sink.Close()
	}
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
	for i := 0; i < n; i++ {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.dlrWorker(d.workerCtx)
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

func (d *Dispatcher) dlrWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case dlr := <-d.dlrCh:
			if err := d.HandleDLR(ctx, dlr); err != nil {
				d.logger.Warn("dlr rejected", "provider", dlr.Provider, "provider_id", dlr.ProviderID, "err", err)
			}
		}
	}
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
			d.FlushDLR("")
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
	msg := provider.OutboundMessage{
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
		RawPayload:         append([]byte(nil), payload.RawPayload...),
		RawPayloadSet:      payload.RawPayloadSet,
		SARRefNum:          append([]byte(nil), payload.SARRefNum...),
		SARTotalSegments:   append([]byte(nil), payload.SARTotalSegments...),
		SARSegmentSeqnum:   append([]byte(nil), payload.SARSegmentSeqnum...),
		SARSet:             payload.SARSet,
	}
	if err := retryStoreTransition(ctx, func(writeCtx context.Context) error {
		return d.store.MarkOutboxSending(writeCtx, item.ID, item.ClaimedBy)
	}); err != nil {
		d.logger.Error("mark outbox sending failed; provider was not called", "outbox_id", item.ID, "gateway_id", payload.GatewayID, "err", err)
		return
	}
	item.State = "sending"
	providerIDs, err := sendProvider(ctx, p, msg)
	if err != nil {
		if isPermanent(err) {
			d.failOutbox(ctx, item, err)
		} else {
			d.markOutboxUncertain(item, err)
		}
		return
	}
	if len(providerIDs) == 0 {
		providerIDs = []string{payload.GatewayID}
	}
	for i, providerID := range providerIDs {
		if providerID == "" {
			providerIDs[i] = payload.GatewayID
		}
	}
	pending := make([]store.Pending, 0, len(providerIDs))
	expiresAt := time.Now().UTC().Add(d.pendingTTL)
	for i, providerID := range providerIDs {
		pending = append(pending, store.Pending{
			ProviderID:         providerID,
			GatewayID:          payload.GatewayID,
			TenantID:           payload.TenantID,
			AccountID:          payload.AccountID,
			ClientMsgID:        payload.ClientMsgID,
			SegmentIndex:       i + 1,
			SegmentCount:       len(providerIDs),
			SourceKind:         payload.SourceKind,
			SourceSession:      payload.SourceSession,
			SourceSystem:       payload.SourceSystem,
			CallbackURL:        payload.CallbackURL,
			CallbackRule:       payload.CallbackRule,
			From:               payload.From,
			To:                 payload.To,
			Text:               payload.Text,
			DataCoding:         payload.DataCoding,
			RegisteredDelivery: payload.RegisteredDelivery,
			Provider:           payload.Provider,
			Route:              payload.Route,
			ReceivedAt:         payload.ReceivedAt,
			ExpiresAt:          expiresAt,
		})
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := retryStoreTransition(persistCtx, func(writeCtx context.Context) error {
		return d.store.CompleteOutboxSend(writeCtx, item.ID, item.ClaimedBy, pending)
	}); err != nil {
		d.logger.Error("complete outbox send failed; message will not be resent", "outbox_id", item.ID, "gateway_id", payload.GatewayID, "err", err)
		d.markOutboxUncertainWithContext(persistCtx, item, fmt.Errorf("persist sent result: %w", err))
		return
	}
	for i, providerID := range providerIDs {
		d.emitCDR(cdr.Event{
			Kind:         "sent",
			GatewayID:    payload.GatewayID,
			ProviderID:   providerID,
			From:         payload.From,
			To:           payload.To,
			TextLen:      len([]rune(payload.Text)),
			TextHash:     cdr.TextHash(payload.Text),
			Encoding:     payload.Encoding,
			Route:        payload.Route,
			Provider:     payload.Provider,
			ClientID:     payload.Meta["client_id"],
			TenantID:     payload.TenantID,
			AccountID:    payload.AccountID,
			ClientMsgID:  payload.ClientMsgID,
			SegmentIndex: i + 1,
			SegmentCount: len(providerIDs),
			SystemID:     payload.SourceSystem,
			Source:       payload.SourceKind,
			State:        "sent",
		})
	}
	d.logger.Info("message dispatched", "gateway_id", payload.GatewayID, "provider_id", providerIDs[0], "provider_id_count", len(providerIDs), "provider", payload.Provider, "route", payload.Route, "source", payload.SourceKind, "source_session", payload.SourceSession, "system_id", payload.SourceSystem, "registered_delivery", payload.RegisteredDelivery)
}

func (d *Dispatcher) markOutboxUncertain(item store.OutboxItem, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.markOutboxUncertainWithContext(ctx, item, err)
}

func (d *Dispatcher) markOutboxUncertainWithContext(ctx context.Context, item store.OutboxItem, err error) {
	if markErr := retryStoreTransition(ctx, func(writeCtx context.Context) error {
		return d.store.MarkOutboxUncertain(writeCtx, item.ID, item.ClaimedBy, err.Error())
	}); markErr != nil {
		d.logger.Error("mark outbox uncertain failed; sending state remains fail-closed", "outbox_id", item.ID, "gateway_id", item.GatewayID, "err", markErr)
	}
	payload := item.Payload
	d.emitCDR(cdr.Event{
		Kind:        "uncertain",
		GatewayID:   payload.GatewayID,
		From:        payload.From,
		To:          payload.To,
		TextLen:     len([]rune(payload.Text)),
		TextHash:    cdr.TextHash(payload.Text),
		Encoding:    payload.Encoding,
		Route:       payload.Route,
		Provider:    payload.Provider,
		ClientID:    payload.Meta["client_id"],
		TenantID:    payload.TenantID,
		AccountID:   payload.AccountID,
		ClientMsgID: payload.ClientMsgID,
		SystemID:    payload.SourceSystem,
		Source:      payload.SourceKind,
		State:       "UNKNOWN",
		ErrorCode:   1,
		Reason:      err.Error(),
	})
}

func retryStoreTransition(ctx context.Context, fn func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(25*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
	return err
}

func (d *Dispatcher) failOutbox(ctx context.Context, item store.OutboxItem, err error) {
	delay := retryDelay(item.Attempt)
	next := time.Now().UTC().Add(delay)
	kind := "retry"
	state := "retry"
	errorCode := 1
	terminal := item.Attempt >= item.MaxAttempts || isPermanent(err)
	var failurePending store.Pending
	var failureDLR provider.DLR
	failureDLRQueued := false
	if terminal {
		next = time.Time{}
		kind = "failed"
		state = "failed"
		failureDLR.State, errorCode = terminalFailureDLR(err)
		_ = d.store.UpdateMessageState(ctx, item.GatewayID, failureDLR.State, errorCode)
		failurePending, failureDLR, failureDLRQueued = d.queueTerminalFailureDLR(ctx, item, failureDLR.State, errorCode)
	}
	d.emitCDR(cdr.Event{
		Kind:        kind,
		GatewayID:   item.Payload.GatewayID,
		From:        item.Payload.From,
		To:          item.Payload.To,
		TextLen:     len([]rune(item.Payload.Text)),
		TextHash:    cdr.TextHash(item.Payload.Text),
		Encoding:    item.Payload.Encoding,
		Route:       item.Payload.Route,
		Provider:    item.Payload.Provider,
		ClientID:    item.Payload.Meta["client_id"],
		TenantID:    item.Payload.TenantID,
		AccountID:   item.Payload.AccountID,
		ClientMsgID: item.Payload.ClientMsgID,
		SystemID:    item.Payload.SourceSystem,
		Source:      item.Payload.SourceKind,
		State:       state,
		ErrorCode:   errorCode,
		Reason:      err.Error(),
	})
	if ferr := d.store.FailOutbox(ctx, item.ID, err.Error(), next); ferr != nil {
		d.logger.Warn("fail outbox failed", "outbox_id", item.ID, "err", ferr)
		if failureDLRQueued {
			_ = d.store.DeletePending(ctx, failurePending.Provider, failurePending.ProviderID)
		}
		return
	}
	if failureDLRQueued {
		d.OnDLR(failureDLR)
	}
}

type smppStatusError interface {
	SMPPStatus() uint32
}

func terminalFailureDLR(err error) (string, int) {
	var statusErr smppStatusError
	if errors.As(err, &statusErr) {
		status := statusErr.SMPPStatus()
		if status > 999 {
			status = 999
		}
		return "REJECTD", int(status)
	}
	return "UNDELIV", 1
}

func (d *Dispatcher) queueTerminalFailureDLR(ctx context.Context, item store.OutboxItem, state string, errorCode int) (store.Pending, provider.DLR, bool) {
	payload := item.Payload
	if payload.SourceKind != SourceSMPP.String() || payload.RegisteredDelivery&0x03 == 0 {
		return store.Pending{}, provider.DLR{}, false
	}
	doneAt := time.Now().UTC()
	receivedAt := payload.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = doneAt
	}
	providerID := "local-failure:" + payload.GatewayID
	rec := store.Pending{
		ProviderID:         providerID,
		GatewayID:          payload.GatewayID,
		TenantID:           payload.TenantID,
		AccountID:          payload.AccountID,
		ClientMsgID:        payload.ClientMsgID,
		SegmentIndex:       1,
		SegmentCount:       1,
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
		ReceivedAt:         receivedAt,
		ExpiresAt:          doneAt.Add(d.pendingTTL),
		DLRReady:           true,
		DLRState:           state,
		DLRErrorCode:       errorCode,
		DLRDoneAt:          doneAt,
	}
	if err := d.store.SavePending(ctx, rec); err != nil {
		d.logger.Error("save terminal failure dlr failed", "gateway_id", payload.GatewayID, "err", err)
		return store.Pending{}, provider.DLR{}, false
	}
	return rec, provider.DLR{
		Provider:   payload.Provider,
		ProviderID: providerID,
		State:      state,
		ErrorCode:  errorCode,
		DoneAt:     doneAt,
	}, true
}

func (d *Dispatcher) emitCDR(e cdr.Event) {
	holder := d.cdrSink.Load()
	if holder == nil || holder.sink == nil {
		return
	}
	if e.Instance == "" {
		if id := d.instanceID.Load(); id != nil {
			e.Instance = *id
		}
	}
	holder.sink.Emit(e)
}

func (d *Dispatcher) dlrEvent(ctx context.Context, rec store.Pending, dlr provider.DLR, aggregate dlrAggregate) cdr.Event {
	clientID := ""
	if msg, ok, err := d.store.GetMessage(ctx, rec.GatewayID); err == nil && ok && msg.Metadata != nil {
		clientID = msg.Metadata["client_id"]
	}
	return cdr.Event{
		Kind:         "dlr",
		GatewayID:    rec.GatewayID,
		ProviderID:   dlr.ProviderID,
		From:         rec.From,
		To:           rec.To,
		TextLen:      len([]rune(rec.Text)),
		TextHash:     cdr.TextHash(rec.Text),
		Route:        rec.Route,
		Provider:     rec.Provider,
		ClientID:     clientID,
		TenantID:     rec.TenantID,
		AccountID:    rec.AccountID,
		ClientMsgID:  rec.ClientMsgID,
		SegmentIndex: rec.SegmentIndex,
		SegmentCount: rec.SegmentCount,
		MessageState: aggregate.State,
		Final:        aggregate.Final,
		SystemID:     rec.SourceSystem,
		Source:       rec.SourceKind,
		State:        dlr.State,
		ErrorCode:    dlr.ErrorCode,
	}
}

func sendProvider(ctx context.Context, p provider.Provider, msg provider.OutboundMessage) ([]string, error) {
	if msg.Context == nil {
		msg.Context = ctx
	}
	if multi, ok := p.(provider.MultiIDProvider); ok {
		return multi.SendAll(msg)
	}
	id, err := p.Send(msg)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	return []string{id}, nil
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
	if rec.SourceSession != "" {
		if s, ok := srv.Session(rec.SourceSession); ok && s.CanReceive() {
			session = s
		}
	}
	if session == nil && rec.SourceSystem != "" {
		receivers := srv.ReceiversBySystemID(rec.SourceSystem)
		if len(receivers) > 0 {
			idx := int(d.dlrPick.Add(1)-1) % len(receivers)
			session = receivers[idx]
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
	d.logger.Info("dlr deliver_sm sending", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "sequence", pdu.SequenceID, "session_id", session.ID(), "system_id", session.SystemID())
	status, ok := session.SendDeliverSM(pdu, 10*time.Second)
	if !ok {
		return errDLRNotAcked
	}
	if status != smpp.StatusOK {
		return fmt.Errorf("%w: status=0x%08x", errDLRNotAcked, status)
	}
	d.logger.Info("dlr deliver_sm acked", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "sequence", pdu.SequenceID, "session_id", session.ID(), "system_id", session.SystemID())
	return nil
}

func (d *Dispatcher) FlushDLR(systemID string) {
	ctx := context.Background()
	for {
		items, err := d.store.ListReadyDLR(ctx, systemID, 500)
		if err != nil {
			d.logger.Warn("list pending dlr failed", "system_id", systemID, "err", err)
			return
		}
		if len(items) == 0 {
			return
		}
		for _, rec := range items {
			unlock := d.lockDLR(rec.Provider, rec.ProviderID)
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
				unlock()
				if !errors.Is(err, errNoReceiverOnline) && !errors.Is(err, errDLRNotAcked) {
					d.logger.Warn("flush deliver_sm failed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "err", err)
				}
				if systemID == "" {
					continue
				}
				return
			}
			if err := d.finishDLRDelivery(ctx, rec); err != nil {
				unlock()
				d.logger.Warn("mark flushed dlr delivered failed", "gateway_id", rec.GatewayID, "provider", rec.Provider, "provider_id", rec.ProviderID, "err", err)
				return
			}
			unlock()
			d.logger.Info("dlr flushed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "system_id", rec.SourceSystem)
		}
		if systemID == "" {
			return
		}
	}
}

func (d *Dispatcher) lockDLR(provider, providerID string) func() {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	hash := offset64
	for _, value := range []string{provider, providerID} {
		for i := 0; i < len(value); i++ {
			hash ^= uint64(value[i])
			hash *= prime64
		}
		hash ^= 0
		hash *= prime64
	}
	lock := &d.dlrLocks[hash%uint64(len(d.dlrLocks))]
	lock.Lock()
	return lock.Unlock
}

func (d *Dispatcher) newGatewayID(ctx context.Context) (string, error) {
	n, err := d.idAlloc.Next(ctx)
	if err != nil {
		return "", err
	}
	encoded := strconv.FormatUint(n, 36)
	if len(encoded) > 7 {
		return "", errors.New("gateway id sequence exhausted")
	}
	return fmt.Sprintf("m%07s", encoded), nil
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

func isFinalDLRState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "REJECTD", "UNKNOWN":
		return true
	default:
		return false
	}
}

type dlrAggregate struct {
	State        string
	ErrorCode    int
	Final        bool
	AllDelivered bool
}

func aggregateDLR(segments []store.Pending) dlrAggregate {
	result := dlrAggregate{State: "PENDING"}
	if len(segments) == 0 {
		return result
	}
	expected := 1
	seen := make(map[int]struct{}, len(segments))
	result.Final = true
	result.AllDelivered = true
	failureRank := 0
	for _, segment := range segments {
		if segment.SegmentCount > expected {
			expected = segment.SegmentCount
		}
		if segment.SegmentIndex > 0 {
			seen[segment.SegmentIndex] = struct{}{}
		}
		state := strings.ToUpper(strings.TrimSpace(segment.DLRState))
		if !isFinalDLRState(state) {
			result.Final = false
		}
		if !segment.DLRDelivered {
			result.AllDelivered = false
		}
		rank := dlrFailureRank(state)
		if rank > failureRank {
			failureRank = rank
			result.State = state
			result.ErrorCode = segment.DLRErrorCode
		}
	}
	if len(seen) < expected {
		result.Final = false
		result.AllDelivered = false
	}
	if !result.Final {
		result.State = "PENDING"
		result.ErrorCode = 0
		return result
	}
	if failureRank == 0 {
		result.State = "DELIVRD"
		result.ErrorCode = 0
	}
	return result
}

func dlrFailureRank(state string) int {
	switch state {
	case "REJECTD":
		return 6
	case "UNDELIV":
		return 5
	case "EXPIRED":
		return 4
	case "DELETED":
		return 3
	case "UNKNOWN":
		return 2
	default:
		return 0
	}
}

func (d *Dispatcher) finishDLRDelivery(ctx context.Context, rec store.Pending) error {
	if err := d.store.MarkDLRDelivered(ctx, rec.Provider, rec.ProviderID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	segments, err := d.store.ListPendingByGatewayID(ctx, rec.GatewayID)
	if err != nil {
		return err
	}
	aggregate := aggregateDLR(segments)
	if aggregate.Final && aggregate.AllDelivered {
		return d.store.DeletePendingByGatewayID(ctx, rec.GatewayID)
	}
	return nil
}

func (d *Dispatcher) sendHTTPCallback(ctx context.Context, rec store.Pending, dlr provider.DLR, aggregate dlrAggregate) error {
	payload := map[string]any{
		"gateway_id":    rec.GatewayID,
		"client_msg_id": rec.ClientMsgID,
		"provider_id":   dlr.ProviderID,
		"provider":      rec.Provider,
		"route":         rec.Route,
		"segment_index": rec.SegmentIndex,
		"segment_count": rec.SegmentCount,
		"state":         dlr.State,
		"message_state": aggregate.State,
		"final":         aggregate.Final,
		"error_code":    dlr.ErrorCode,
		"done_at":       dlr.DoneAt.Format(time.RFC3339Nano),
	}
	if rec.CallbackRule != "" {
		payload["callback_rule"] = rec.CallbackRule
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rec.CallbackURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback status %d", resp.StatusCode)
	}
	return nil
}
