package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Expiry applies only to newly managed messages. Historical pending records
// keep their existing cleanup policy; deployment does not replay old HTTP DLRs.
func expireReceiptGroup(rows []Pending, now time.Time) ([]Pending, []ReceiptJob, bool) {
	if len(rows) == 0 {
		return rows, nil, false
	}
	allExpired := true
	seen := map[int]bool{}
	expected := 1
	for _, p := range rows {
		if p.ExpiresAt.After(now) {
			allExpired = false
		}
		seen[p.SegmentIndex] = true
		if p.SegmentCount > expected {
			expected = p.SegmentCount
		}
	}
	if allExpired {
		for i := 1; i <= expected; i++ {
			if !seen[i] {
				p := rows[0]
				p.ProviderID = fmt.Sprintf("local-expired:%s:%d", p.GatewayID, i)
				p.SegmentIndex = i
				p.DLRState = ""
				p.DLRDelivered = false
				p.DLRReady = false
				rows = append(rows, p)
			}
		}
	}
	var changed []int
	for i, p := range rows {
		if !p.ExpiresAt.After(now) && !IsFinalReceiptState(p.DLRState) {
			p.DLRState = "EXPIRED"
			p.DLRErrorCode = 1
			p.DLRDoneAt = now
			p.DLRDelivered = false
			p.DLRReady = false
			rows[i] = p
			changed = append(changed, i)
		}
	}
	var jobs []ReceiptJob
	for _, i := range changed {
		p := rows[i]
		jobs = append(jobs, deliveryJob(p, ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: p.DLRState, ErrorCode: p.DLRErrorCode, DoneAt: p.DLRDoneAt}, rows))
	}
	return rows, jobs, allExpired
}

func (s *MemoryStore) expireManagedLocked(now time.Time) int {
	groups := map[string][]Pending{}
	for _, p := range s.pending {
		if p.ReliabilityManaged {
			groups[p.GatewayID] = append(groups[p.GatewayID], p)
		}
	}
	removed := 0
	for id, rows := range groups {
		rows, jobs, remove := expireReceiptGroup(rows, now)
		for _, j := range jobs {
			if _, ok := s.receipts[j.ID]; !ok {
				s.receipts[j.ID] = j
			}
		}
		if agg := AggregateReceipt(rows); agg.Final {
			if idx, ok := s.messageByID[id]; ok {
				s.messages[idx].State = agg.State
				s.messages[idx].ErrorCode = agg.ErrorCode
				if s.messages[idx].DoneAt.IsZero() {
					s.messages[idx].DoneAt = now
				}
			}
		}
		for _, p := range rows {
			key := pendingKey(p.Provider, p.ProviderID)
			if remove {
				delete(s.pending, key)
				removed++
			} else {
				s.pending[key] = p
			}
		}
	}
	return removed
}

func (s *PostgresStore) expireManaged(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT gateway_id FROM pending WHERE reliability_managed=TRUE AND expires_at<=$1 LIMIT 1000`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		n, err := s.expireManagedGroup(ctx, id, now)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

func (s *PostgresStore) expireManagedGroup(ctx context.Context, id string, now time.Time) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT gateway_id FROM messages WHERE gateway_id=$1 FOR UPDATE`, id); err != nil {
		return 0, err
	}
	result, err := tx.Query(ctx, `SELECT `+pendingSelectColumns+` FROM pending WHERE gateway_id=$1 AND reliability_managed=TRUE ORDER BY provider,provider_id FOR UPDATE`, id)
	if err != nil {
		return 0, err
	}
	rows, err := pgx.CollectRows(result, scanPending)
	if err != nil {
		return 0, err
	}
	rows, jobs, remove := expireReceiptGroup(rows, now)
	for _, j := range jobs {
		if _, err = tx.Exec(ctx, `INSERT INTO receipt_jobs(id,kind,payload,next_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, j.ID, j.Kind, j.Payload, j.NextAt, j.ExpiresAt); err != nil {
			return 0, err
		}
	}
	if agg := AggregateReceipt(rows); agg.Final {
		if _, err = tx.Exec(ctx, `UPDATE messages SET state=$2,error_code=$3,done_at=COALESCE(done_at,$4) WHERE gateway_id=$1`, id, agg.State, agg.ErrorCode, now); err != nil {
			return 0, err
		}
	}
	n := 0
	if remove {
		tag, err := tx.Exec(ctx, `DELETE FROM pending WHERE gateway_id=$1 AND reliability_managed=TRUE`, id)
		if err != nil {
			return 0, err
		}
		n = int(tag.RowsAffected())
	} else {
		for _, p := range rows {
			if err = savePendingSQL(ctx, tx, p); err != nil {
				return 0, err
			}
		}
	}
	return n, tx.Commit(ctx)
}
