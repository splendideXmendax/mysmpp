package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrMultipartConflict = errors.New("multipart reference reused, expired, or inconsistent")
var ErrLeaseLost = errors.New("receipt job lease lost")

// MultipartBinding pins the entire routing decision, including address rewrite.
// Reservation precedes admission and never debits quota. A rejected submission
// can retry the same part, but cannot reuse the reference for different content.
type MultipartBinding struct {
	Key       string
	Route     json.RawMessage
	Provider  string
	Total     int
	Parts     map[int]string
	ExpiresAt time.Time
}

// ReceiptJob stores either a received upstream event or an immutable customer
// delivery snapshot. Done/dead rows are retained for deduplication and audit.
type ReceiptJob struct {
	ID         string
	Kind       string
	Payload    json.RawMessage
	State      string
	Attempt    int
	Lease      int64
	LeaseUntil time.Time
	NextAt     time.Time
	ExpiresAt  time.Time
	LastError  string
}

func mergeMultipart(old MultipartBinding, found bool, proposed MultipartBinding, part int, fingerprint string, now time.Time) (MultipartBinding, error) {
	if found && !old.ExpiresAt.After(now) {
		if part != 1 {
			return MultipartBinding{}, ErrMultipartConflict
		}
		found = false
	}
	if !found {
		if len(proposed.Route) == 0 || proposed.Provider == "" {
			return MultipartBinding{}, ErrNotFound
		}
		old = proposed
		old.Parts = map[int]string{}
	}
	if old.Total != proposed.Total || part < 1 || part > old.Total {
		return MultipartBinding{}, ErrMultipartConflict
	}
	if fp, ok := old.Parts[part]; ok && fp != fingerprint {
		return MultipartBinding{}, ErrMultipartConflict
	}
	old.Parts[part] = fingerprint
	return old, nil
}

func (s *MemoryStore) BindMultipart(_ context.Context, proposed MultipartBinding, part int, fingerprint string) (MultipartBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, found := s.multipart[proposed.Key]
	// Copy before modifying so returned data cannot race the next submission.
	old = cloneBinding(old)
	binding, err := mergeMultipart(old, found, proposed, part, fingerprint, time.Now().UTC())
	if err != nil {
		return MultipartBinding{}, err
	}
	s.multipart[proposed.Key] = cloneBinding(binding)
	return binding, nil
}

func cloneBinding(b MultipartBinding) MultipartBinding {
	b.Route = append(json.RawMessage(nil), b.Route...)
	p := make(map[int]string, len(b.Parts))
	for k, v := range b.Parts {
		p[k] = v
	}
	b.Parts = p
	return b
}

func normalizeJob(j ReceiptJob) ReceiptJob {
	j.State = "pending"
	j.Attempt, j.Lease = 0, 0
	j.LeaseUntil = time.Time{}
	if j.NextAt.IsZero() {
		j.NextAt = time.Now().UTC()
	}
	if j.ExpiresAt.IsZero() {
		j.ExpiresAt = j.NextAt.Add(48 * time.Hour)
	}
	j.Payload = append(json.RawMessage(nil), j.Payload...)
	return j
}

func (s *MemoryStore) EnqueueReceipt(_ context.Context, job ReceiptJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.receipts[job.ID]; !found {
		s.receipts[job.ID] = normalizeJob(job)
	}
	return nil
}

func (s *MemoryStore) ClaimReceipt(_ context.Context, kind string, now time.Time) (ReceiptJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.receipts {
		if j.Kind != kind || (j.State != "pending" && j.State != "claimed") {
			continue
		}
		if j.State == "claimed" && j.LeaseUntil.After(now) {
			continue
		}
		if !j.ExpiresAt.After(now) {
			j.State = "dead"
			s.receipts[id] = j
			continue
		}
		if j.NextAt.After(now) {
			continue
		}
		j.State = "claimed"
		j.Attempt++
		j.Lease++
		j.LeaseUntil = now.Add(time.Minute)
		s.receipts[id] = j
		j.Payload = append(json.RawMessage(nil), j.Payload...)
		return j, true, nil
	}
	return ReceiptJob{}, false, nil
}

func (s *MemoryStore) FinishReceipt(_ context.Context, job ReceiptJob, next time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.receipts[job.ID]
	if !ok || j.State != "claimed" || j.Lease != job.Lease {
		return ErrLeaseLost
	}
	j.State = "done"
	if reason != "" {
		j.State = "pending"
		if next.IsZero() || !next.Before(j.ExpiresAt) || j.Attempt >= 1000 {
			j.State = "dead"
		}
	}
	j.NextAt, j.LastError = next, reason
	if j.State == "done" {
		j.Payload = json.RawMessage(`{}`)
	}
	s.receipts[job.ID] = j
	return nil
}

func (s *MemoryStore) ReceiptCounts(_ context.Context) (map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	for _, j := range s.receipts {
		counts[j.Kind+":"+j.State]++
	}
	return counts, nil
}

// SweepReliability bounds receipt deduplication and concatenation tombstones.
func (s *MemoryStore) SweepReliability(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, b := range s.multipart {
		if b.ExpiresAt.Add(7 * 24 * time.Hour).Before(now) {
			delete(s.multipart, k)
		}
	}
	for k, j := range s.receipts {
		if j.ExpiresAt.Before(now) && (j.State == "pending" || (j.State == "claimed" && j.LeaseUntil.Before(now))) {
			j.State = "dead"
			s.receipts[k] = j
		}
		if (j.State == "done" && j.ExpiresAt.Before(now)) || j.ExpiresAt.Add(7*24*time.Hour).Before(now) {
			delete(s.receipts, k)
		}
	}
	return nil
}
