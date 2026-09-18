package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func (d *Dispatcher) EnqueueDLR(ctx context.Context, dlr provider.DLR) error {
	if dlr.Provider == "" || dlr.ProviderID == "" {
		return errors.New("receipt requires provider and provider ID")
	}
	e := store.ReceiptEvent{Provider: dlr.Provider, ProviderID: dlr.ProviderID, State: strings.ToUpper(strings.TrimSpace(dlr.State)), ErrorCode: dlr.ErrorCode, DoneAt: dlr.DoneAt.UTC()}
	if e.ErrorCode < 0 || int64(e.ErrorCode) > 2147483647 {
		return errors.New("receipt error code is out of range")
	}
	switch e.State {
	case "ENROUTE", "ACCEPTD", "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "REJECTD", "UNKNOWN":
	default:
		return errors.New("invalid receipt state")
	}
	// Fingerprint the source event before assigning a fallback timestamp; an
	// upstream that omits done_date must still deduplicate across reconnects.
	id := store.ReceiptKey("inbox", e)
	if e.DoneAt.IsZero() {
		e.DoneAt = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return d.store.EnqueueReceipt(ctx, store.ReceiptJob{ID: id, Kind: "inbox", Payload: b, ExpiresAt: time.Now().UTC().Add(d.pendingTTL)})
}

func (d *Dispatcher) receiptWorker(ctx context.Context, kind string) {
	const base = 100 * time.Millisecond
	delay := base
	for {
		if !waitPoll(ctx, delay) {
			return
		}
		for ctx.Err() == nil {
			job, ok, err := d.store.ClaimReceipt(ctx, kind, time.Now().UTC())
			if err != nil {
				if ctx.Err() == nil {
					d.logger.Warn("claim receipt failed", "kind", kind, "err", err)
				}
				delay = idlePollDelay(delay, base)
				break
			}
			if !ok {
				delay = idlePollDelay(delay, base)
				break
			}
			delay = base
			workCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			if kind == "inbox" {
				var e store.ReceiptEvent
				err = json.Unmarshal(job.Payload, &e)
				if err == nil {
					err = d.store.PrepareReceipt(workCtx, e)
				}
			} else {
				err = d.deliverReceipt(workCtx, job)
			}
			cancel()
			next := time.Time{}
			reason := ""
			if err != nil {
				reason = err.Error()
				delay := retryDelay(job.Attempt)
				if job.Attempt > 6 {
					delay = time.Duration(1<<uint(min(job.Attempt-1, 9))) * time.Second
				}
				if delay > 5*time.Minute {
					delay = 5 * time.Minute
				}
				next = time.Now().UTC().Add(delay)
				if errors.Is(err, errUnsafeCallback) {
					next = time.Time{}
				}
			}
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 3*time.Second)
			finishErr := d.store.FinishReceipt(finishCtx, job, next, reason)
			finishCancel()
			if finishErr != nil {
				d.logger.Warn("finish receipt failed", "job_id", job.ID, "err", finishErr)
			}
			if err != nil && job.Attempt == 1 {
				d.logger.Warn("receipt deferred", "job_id", job.ID, "kind", kind, "err", err)
			}
		}
	}
}

func (d *Dispatcher) deliverReceipt(ctx context.Context, job store.ReceiptJob) error {
	var delivery store.ReceiptDelivery
	if err := json.Unmarshal(job.Payload, &delivery); err != nil {
		return err
	}
	rec, e := delivery.Pending, delivery.Event
	current, found, err := d.store.GetPending(ctx, rec.Provider, rec.ProviderID)
	if err != nil {
		return err
	}
	if found && current.DLRState != rec.DLRState && store.IsFinalReceiptState(current.DLRState) {
		return nil
	} // superseded interim event
	dlr := provider.DLR{Provider: e.Provider, ProviderID: e.ProviderID, State: e.State, ErrorCode: e.ErrorCode, DoneAt: e.DoneAt}
	switch rec.SourceKind {
	case SourceHTTPAPI.String():
		if rec.CallbackURL != "" {
			err = d.sendHTTPCallback(ctx, rec, dlr, delivery.Aggregate)
		}
	case SourceSMPP.String():
		if rec.RegisteredDelivery&3 != 0 {
			err = d.pushSMPPDLR(rec, dlr)
		}
	}
	if err != nil {
		return err
	}
	if err = d.store.MarkReceiptDelivered(ctx, rec); err != nil {
		return err
	}
	d.emitCDR(d.dlrEvent(ctx, rec, dlr, delivery.Aggregate))
	return nil
}
