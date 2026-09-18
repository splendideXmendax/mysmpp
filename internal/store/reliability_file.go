package store

import (
	"context"
	"time"
)

func (s *FileStore) PrepareReceipt(ctx context.Context, e ReceiptEvent) error {
	if err := s.MemoryStore.PrepareReceipt(ctx, e); err != nil {
		return err
	}
	return s.persist()
}
func (s *FileStore) MarkReceiptDelivered(ctx context.Context, p Pending) error {
	if err := s.MemoryStore.MarkReceiptDelivered(ctx, p); err != nil {
		return err
	}
	return s.persist()
}

func (s *FileStore) BindMultipart(ctx context.Context, b MultipartBinding, part int, fp string) (MultipartBinding, error) {
	b, err := s.MemoryStore.BindMultipart(ctx, b, part, fp)
	if err != nil {
		return b, err
	}
	return b, s.persist()
}
func (s *FileStore) EnqueueReceipt(ctx context.Context, j ReceiptJob) error {
	if err := s.MemoryStore.EnqueueReceipt(ctx, j); err != nil {
		return err
	}
	return s.persist()
}
func (s *FileStore) ClaimReceipt(ctx context.Context, kind string, now time.Time) (ReceiptJob, bool, error) {
	j, ok, err := s.MemoryStore.ClaimReceipt(ctx, kind, now)
	if err != nil || !ok {
		return j, ok, err
	}
	return j, ok, s.persist()
}
func (s *FileStore) FinishReceipt(ctx context.Context, j ReceiptJob, next time.Time, reason string) error {
	if err := s.MemoryStore.FinishReceipt(ctx, j, next, reason); err != nil {
		return err
	}
	return s.persist()
}
func (s *FileStore) SweepReliability(ctx context.Context, now time.Time) error {
	if err := s.MemoryStore.SweepReliability(ctx, now); err != nil {
		return err
	}
	return s.persist()
}
