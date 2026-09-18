package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type ReceiptEvent struct {
	Provider   string
	ProviderID string
	State      string
	ErrorCode  int
	DoneAt     time.Time
}

type ReceiptDelivery struct {
	Pending   Pending
	Event     ReceiptEvent
	Aggregate ReceiptAggregate
}

func ReceiptKey(kind string, event ReceiptEvent) string {
	b, _ := json.Marshal(event)
	h := sha256.Sum256(b)
	return kind + ":" + hex.EncodeToString(h[:])
}

func applyReceiptEvent(p Pending, event ReceiptEvent) (Pending, bool) {
	if !p.DLRReady && (IsFinalReceiptState(p.DLRState) || (!p.DLRDoneAt.IsZero() && p.DLRState == event.State && p.DLRErrorCode == event.ErrorCode)) {
		return p, false
	}
	p.DLRState = strings.ToUpper(strings.TrimSpace(event.State))
	p.DLRErrorCode = event.ErrorCode
	p.DLRDoneAt = event.DoneAt
	p.DLRDelivered = false
	p.DLRReady = false // durable delivery job owns retries, not legacy FlushDLR
	return p, true
}

func deliveryJob(p Pending, event ReceiptEvent, segments []Pending) ReceiptJob {
	payload, _ := json.Marshal(ReceiptDelivery{Pending: p, Event: event, Aggregate: AggregateReceipt(segments)})
	return normalizeJob(ReceiptJob{ID: ReceiptKey("delivery", event), Kind: "delivery", Payload: payload, ExpiresAt: time.Now().UTC().Add(48 * time.Hour)})
}

func completionState(pending []Pending) string {
	state := "done"
	for _, p := range pending {
		if p.DLRReady {
			if p.DLRState == "UNKNOWN" {
				return "uncertain"
			}
			state = "failed"
		}
	}
	return state
}

func completionJobs(pending []Pending) []ReceiptJob {
	var jobs []ReceiptJob
	for i, p := range pending {
		if !p.DLRReady {
			continue
		}
		e := ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: p.DLRState, ErrorCode: p.DLRErrorCode, DoneAt: p.DLRDoneAt}
		p.DLRReady = false
		pending[i] = p
		jobs = append(jobs, deliveryJob(p, e, pending))
	}
	return jobs
}

// PrepareReceipt atomically updates aggregation and snapshots the customer
// delivery. Upstream acknowledgement only requires EnqueueReceipt to commit.
func (s *MemoryStore) PrepareReceipt(_ context.Context, event ReceiptEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pendingKey(event.Provider, event.ProviderID)
	p, ok := s.pending[key]
	if !ok {
		return ErrNotFound
	}
	p, changed := applyReceiptEvent(p, event)
	if !changed {
		return nil
	}
	var segments []Pending
	for k, v := range s.pending {
		if v.GatewayID == p.GatewayID {
			if k == key {
				v = p
			}
			segments = append(segments, v)
		}
	}
	j := deliveryJob(p, event, segments)
	agg := AggregateReceipt(segments)
	if agg.Final {
		if idx, ok := s.messageByID[p.GatewayID]; ok {
			s.messages[idx].State = agg.State
			s.messages[idx].ErrorCode = agg.ErrorCode
			s.messages[idx].DoneAt = event.DoneAt
		}
	}
	s.pending[key] = p
	if _, exists := s.receipts[j.ID]; !exists {
		s.receipts[j.ID] = j
	}
	return nil
}

func (s *MemoryStore) MarkReceiptDelivered(_ context.Context, rec Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pendingKey(rec.Provider, rec.ProviderID)
	p, ok := s.pending[key]
	if !ok {
		return nil
	}
	if p.DLRState == rec.DLRState && p.DLRDoneAt.Equal(rec.DLRDoneAt) {
		p.DLRDelivered = true
		p.DLRReady = false
		s.pending[key] = p
	}
	return nil
}
