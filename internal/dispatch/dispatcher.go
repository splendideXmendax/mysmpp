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
	logger        *slog.Logger
	registry      *provider.Registry
	router        atomic.Pointer[router.Router]
	filterEngine  atomic.Pointer[filter.Engine]
	cdrSink       atomic.Pointer[cdrSinkHolder]
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
	dlrLookupWait time.Duration
	workerCtx     context.Context
	cancelWorkers context.CancelFunc
	wg            sync.WaitGroup
	httpClient    *http.Client
	dlrCh         chan provider.DLR
	instanceID    atomic.Pointer[string]

	mu      sync.RWMutex
	smppSrv SMPPServer
}

type cdrSinkHolder struct {
	sink CDRSink
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

func (d *Dispatcher) SetFilterEngine(e *filter.Engine) {
	d.filterEngine.Store(e)
}

func (d *Dispatcher) ReloadFilter(e *filter.Engine) {
	d.SetFilterEngine(e)
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
	env.To = strings.TrimPrefix(env.To, "+")
	filterDecision := filter.Decision{Action: filter.ActionPass, NewText: env.Text}
	if engine := d.filterEngine.Load(); engine != nil {
		filterDecision = engine.Evaluate(env.Text)
		if filterDecision.Action == filter.ActionBlock {
			d.emitCDR(cdr.Event{
				Kind:       "rejected",
				From:       env.From,
				To:         env.To,
				TextLen:    len([]rune(env.Text)),
				TextHash:   cdr.TextHash(env.Text),
				ClientID:   env.ClientID,
				SystemID:   env.Source.SMPPSystemID,
				Source:     env.Source.Kind.String(),
				Reason:     "filter_block",
				FilterRule: filterDecision.Reason,
			})
			return Receipt{}, fmt.Errorf("%w: %s", ErrBlocked, filterDecision.Reason)
		}
		if filterDecision.Action == filter.ActionMask {
			if len(env.UDH) > 0 || env.SARSet {
				d.emitCDR(cdr.Event{
					Kind:       "rejected",
					From:       env.From,
					To:         env.To,
					TextLen:    len([]rune(env.Text)),
					TextHash:   cdr.TextHash(env.Text),
					ClientID:   env.ClientID,
					SystemID:   env.Source.SMPPSystemID,
					Source:     env.Source.Kind.String(),
					Reason:     "filter_mask_multipart",
					FilterRule: filterDecision.Reason,
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
	match, ok := rt.MatchSubmit(router.MatchInput{
		To:       env.To,
		From:     env.From,
		SystemID: env.Source.SMPPSystemID,
		ClientID: env.ClientID,
		Tags:     filterDecision.Tags,
		Now:      env.ReceivedAt,
	})
	if !ok {
		d.emitCDR(cdr.Event{
			Kind:     "rejected",
			From:     env.From,
			To:       env.To,
			TextLen:  len([]rune(env.Text)),
			TextHash: cdr.TextHash(env.Text),
			ClientID: env.ClientID,
			SystemID: env.Source.SMPPSystemID,
			Source:   env.Source.Kind.String(),
			Reason:   "no_route",
		})
		return Receipt{}, fmt.Errorf("%w %q", ErrNoRoute, env.To)
	}
	route := match.Route
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
				Kind:     "rejected",
				From:     env.From,
				To:       env.To,
				TextLen:  len([]rune(env.Text)),
				TextHash: cdr.TextHash(env.Text),
				ClientID: env.ClientID,
				SystemID: env.Source.SMPPSystemID,
				Source:   env.Source.Kind.String(),
				Route:    route.Name,
				Provider: match.Provider,
				Reason:   "bad_dest",
			})
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
	msg.Provider = match.Provider
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
	payloadMeta := cloneMeta(env.Meta)
	if env.ClientID != "" {
		if payloadMeta == nil {
			payloadMeta = map[string]string{}
		}
		payloadMeta["client_id"] = env.ClientID
	}
	payload := store.OutboxPayload{
		GatewayID:          gatewayID,
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
	_, existingGatewayID, duplicate, err := d.store.SubmitAtomic(ctx, msg, store.OutboxItem{
		GatewayID:   gatewayID,
		Provider:    match.Provider,
		Payload:     payload,
		MaxAttempts: d.maxAttempts,
	}, env.ClientID, env.ClientMsgID, 24*time.Hour)
	if err != nil {
		return Receipt{}, err
	}
	if duplicate {
		return d.receiptForExisting(ctx, existingGatewayID, route)
	}
	d.emitCDR(cdr.Event{
		Kind:      "accepted",
		GatewayID: gatewayID,
		From:      env.From,
		To:        env.To,
		TextLen:   len([]rune(env.Text)),
		TextHash:  cdr.TextHash(env.Text),
		Encoding:  encoding,
		Segments:  len(msg.Segments),
		Route:     route.Name,
		Provider:  match.Provider,
		ClientID:  env.ClientID,
		SystemID:  env.Source.SMPPSystemID,
		Source:    env.Source.Kind.String(),
		State:     "queued",
	})
	d.logger.Info("message queued", "gateway_id", gatewayID, "provider", match.Provider, "route", route.Name, "source", env.Source.Kind.String(), "source_session", env.Source.SMPPSessionID, "system_id", env.Source.SMPPSystemID, "registered_delivery", env.RegisteredDelivery)
	return Receipt{GatewayID: gatewayID, Provider: match.Provider, Route: route.Name, State: "queued"}, nil
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
	rec, ok, err := d.getPendingForDLR(ctx, dlr.ProviderID)
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
	d.emitCDR(d.dlrEvent(ctx, rec, dlr))
	if rec.SourceKind == SourceSMPP.String() && rec.RegisteredDelivery&0x03 == 0 {
		d.logger.Info("dlr skipped, registered_delivery not requested", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "registered_delivery", rec.RegisteredDelivery, "source_session", rec.SourceSession, "system_id", rec.SourceSystem)
		_ = d.store.DeletePending(ctx, dlr.ProviderID)
		return nil
	}
	switch rec.SourceKind {
	case SourceSMPP.String():
		if err := d.pushSMPPDLR(rec, dlr); err != nil {
			if errors.Is(err, errNoReceiverOnline) || errors.Is(err, errDLRNotAcked) {
				if markErr := d.store.MarkDLRReady(ctx, dlr.ProviderID, dlr.State, dlr.ErrorCode, dlr.DoneAt); markErr != nil {
					d.logger.Warn("mark dlr pending failed", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "err", markErr)
					return markErr
				}
				d.logger.Info("dlr deferred", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "system_id", rec.SourceSystem, "reason", err)
				return nil
			}
			d.logger.Warn("send deliver_sm failed", "gateway_id", rec.GatewayID, "err", err)
			return err
		}
		if isFinalDLRState(dlr.State) {
			_ = d.store.DeletePending(ctx, dlr.ProviderID)
		}
	case SourceHTTPAPI.String():
		if rec.CallbackURL != "" {
			if err := d.sendHTTPCallback(ctx, rec, dlr); err != nil {
				d.logger.Warn("http dlr callback failed", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "url", rec.CallbackURL, "err", err)
				return err
			}
			d.logger.Info("http dlr callback sent", "gateway_id", rec.GatewayID, "provider_id", dlr.ProviderID, "url", rec.CallbackURL)
		} else {
			d.logger.Info("dlr for http source without callback", "gateway_id", rec.GatewayID, "state", dlr.State)
		}
		if isFinalDLRState(dlr.State) {
			_ = d.store.DeletePending(ctx, dlr.ProviderID)
		}
	}
	return nil
}

func (d *Dispatcher) getPendingForDLR(ctx context.Context, providerID string) (store.Pending, bool, error) {
	deadline := time.Now().Add(d.dlrLookupWait)
	for {
		rec, ok, err := d.store.GetPending(ctx, providerID)
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
	providerIDs, err := sendProvider(ctx, p, msg)
	if err != nil {
		d.failOutbox(ctx, item, err)
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
	if err := d.store.UpdateMessageSent(ctx, payload.GatewayID, providerIDs[0]); err != nil {
		d.logger.Warn("update sent message failed", "gateway_id", payload.GatewayID, "err", err)
	}
	for _, providerID := range providerIDs {
		if err := d.store.SavePending(ctx, store.Pending{
			ProviderID:         providerID,
			GatewayID:          payload.GatewayID,
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
			ExpiresAt:          time.Now().UTC().Add(d.pendingTTL),
		}); err != nil {
			d.logger.Error("save pending failed, will requeue", "gateway_id", payload.GatewayID, "provider_id", providerID, "err", err)
			d.failOutbox(ctx, item, fmt.Errorf("save pending: %w", err))
			return
		}
		d.emitCDR(cdr.Event{
			Kind:       "sent",
			GatewayID:  payload.GatewayID,
			ProviderID: providerID,
			From:       payload.From,
			To:         payload.To,
			TextLen:    len([]rune(payload.Text)),
			TextHash:   cdr.TextHash(payload.Text),
			Encoding:   payload.Encoding,
			Route:      payload.Route,
			Provider:   payload.Provider,
			ClientID:   payload.Meta["client_id"],
			SystemID:   payload.SourceSystem,
			Source:     payload.SourceKind,
			State:      "sent",
		})
	}
	if err := d.store.AckOutbox(ctx, item.ID); err != nil {
		d.logger.Warn("ack outbox failed", "outbox_id", item.ID, "err", err)
	}
	d.logger.Info("message dispatched", "gateway_id", payload.GatewayID, "provider_id", providerIDs[0], "provider_id_count", len(providerIDs), "provider", payload.Provider, "route", payload.Route, "source", payload.SourceKind, "source_session", payload.SourceSession, "system_id", payload.SourceSystem, "registered_delivery", payload.RegisteredDelivery)
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
		Kind:      kind,
		GatewayID: item.Payload.GatewayID,
		From:      item.Payload.From,
		To:        item.Payload.To,
		TextLen:   len([]rune(item.Payload.Text)),
		TextHash:  cdr.TextHash(item.Payload.Text),
		Encoding:  item.Payload.Encoding,
		Route:     item.Payload.Route,
		Provider:  item.Payload.Provider,
		ClientID:  item.Payload.Meta["client_id"],
		SystemID:  item.Payload.SourceSystem,
		Source:    item.Payload.SourceKind,
		State:     state,
		ErrorCode: errorCode,
		Reason:    err.Error(),
	})
	if ferr := d.store.FailOutbox(ctx, item.ID, err.Error(), next); ferr != nil {
		d.logger.Warn("fail outbox failed", "outbox_id", item.ID, "err", ferr)
		if failureDLRQueued {
			_ = d.store.DeletePending(ctx, failurePending.ProviderID)
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

func (d *Dispatcher) dlrEvent(ctx context.Context, rec store.Pending, dlr provider.DLR) cdr.Event {
	clientID := ""
	if msg, ok, err := d.store.GetMessage(ctx, rec.GatewayID); err == nil && ok && msg.Metadata != nil {
		clientID = msg.Metadata["client_id"]
	}
	return cdr.Event{
		Kind:       "dlr",
		GatewayID:  rec.GatewayID,
		ProviderID: dlr.ProviderID,
		From:       rec.From,
		To:         rec.To,
		TextLen:    len([]rune(rec.Text)),
		TextHash:   cdr.TextHash(rec.Text),
		Route:      rec.Route,
		Provider:   rec.Provider,
		ClientID:   clientID,
		SystemID:   rec.SourceSystem,
		Source:     rec.SourceKind,
		State:      dlr.State,
		ErrorCode:  dlr.ErrorCode,
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
				if !errors.Is(err, errNoReceiverOnline) && !errors.Is(err, errDLRNotAcked) {
					d.logger.Warn("flush deliver_sm failed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "err", err)
				}
				if systemID == "" {
					continue
				}
				return
			}
			_ = d.store.DeletePending(ctx, rec.ProviderID)
			d.logger.Info("dlr flushed", "gateway_id", rec.GatewayID, "provider_id", rec.ProviderID, "system_id", rec.SourceSystem)
		}
		if systemID == "" {
			return
		}
	}
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

func (d *Dispatcher) sendHTTPCallback(ctx context.Context, rec store.Pending, dlr provider.DLR) error {
	payload := map[string]any{
		"gateway_id":  rec.GatewayID,
		"provider_id": dlr.ProviderID,
		"provider":    rec.Provider,
		"route":       rec.Route,
		"state":       dlr.State,
		"error_code":  dlr.ErrorCode,
		"done_at":     dlr.DoneAt.Format(time.RFC3339Nano),
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
