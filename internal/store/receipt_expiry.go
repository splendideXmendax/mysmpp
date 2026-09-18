package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
	blocked := map[string]bool{}
	for _, j := range s.receipts {
		if j.Kind == "inbox" && (j.State == "pending" || j.State == "claimed") {
			var e ReceiptEvent
			if json.Unmarshal(j.Payload, &e) == nil {
				if p, ok := s.pending[pendingKey(e.Provider, e.ProviderID)]; ok {
					blocked[p.GatewayID] = true
				}
			}
		}
	}
	groups := map[string][]Pending{}
	for _, p := range s.pending {
		if p.ReliabilityManaged && !blocked[p.GatewayID] {
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
	deadline := time.Now().Add(15 * time.Second)
	// Already-terminal complete groups need no synthetic events or per-message
	// transactions. Existing delivery snapshots remain independently durable.
	removed, err := s.drainMaintenance(ctx, `DELETE FROM pending WHERE gateway_id IN (
 SELECT gateway_id FROM pending WHERE reliability_managed=TRUE AND expires_at<=$1 GROUP BY gateway_id
 HAVING bool_and(reliability_managed) AND max(expires_at)<=$1
 AND bool_and(COALESCE(dlr_state,'') IN ('DELIVRD','EXPIRED','DELETED','UNDELIV','REJECTD','UNKNOWN'))
 AND count(DISTINCT segment_index)>=GREATEST(max(segment_count),1)
 AND NOT EXISTS (SELECT 1 FROM pending p WHERE p.gateway_id=pending.gateway_id AND (p.expires_at>$1 OR NOT p.reliability_managed))
 AND NOT EXISTS (SELECT 1 FROM pending p JOIN receipt_jobs j
 ON j.payload->>'Provider'=p.provider AND j.payload->>'ProviderID'=p.provider_id
 WHERE p.gateway_id=pending.gateway_id AND j.kind='inbox' AND j.state IN ('pending','claimed'))
 LIMIT 10000)`, deadline, now)
	if err != nil {
		return removed, err
	}
	// Keyset traversal prevents a blocked group from starving other expired
	// groups. Recheck inbox under the message lock in expireManagedGroup.
	cursor := ""
	for time.Now().Before(deadline) {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT gateway_id FROM pending WHERE reliability_managed=TRUE AND expires_at<=$1 AND gateway_id>$2
 AND NOT EXISTS (SELECT 1 FROM pending p JOIN receipt_jobs j
 ON j.payload->>'Provider'=p.provider AND j.payload->>'ProviderID'=p.provider_id
 WHERE p.gateway_id=pending.gateway_id AND j.kind='inbox' AND j.state IN ('pending','claimed'))
 ORDER BY gateway_id LIMIT 1000`, now, cursor)
		if err != nil {
			return removed, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return removed, err
		}
		if len(ids) == 0 {
			break
		}
		n, err := s.expireManagedPage(ctx, ids, now, deadline)
		removed += n
		if err != nil {
			return removed, err
		}
		cursor = ids[len(ids)-1]
		if len(ids) < 1000 {
			break
		}
	}
	return removed, nil
}

func (s *PostgresStore) expireManagedPage(ctx context.Context, ids []string, now, deadline time.Time) (int, error) {
	// Independent message transactions may run concurrently. Keep maintenance
	// bounded to four connections so customer submissions retain pool capacity.
	const workers = 4
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var counts [workers]int
	var errs [workers]error
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < len(ids) && ctx.Err() == nil && time.Now().Before(deadline); i += workers {
				n, err := s.expireManagedGroup(ctx, ids[i], now)
				counts[worker] += n
				if err != nil {
					errs[worker] = err
					cancel()
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	removed := 0
	var err error
	for i := range counts {
		removed += counts[i]
		if errs[i] != nil {
			err = errs[i]
		}
	}
	return removed, err
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
	var waiting bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pending p JOIN receipt_jobs j
 ON j.payload->>'Provider'=p.provider AND j.payload->>'ProviderID'=p.provider_id
 WHERE p.gateway_id=$1 AND j.kind='inbox' AND j.state IN ('pending','claimed'))`, id).Scan(&waiting); err != nil {
		return 0, err
	}
	if waiting {
		return 0, nil
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
