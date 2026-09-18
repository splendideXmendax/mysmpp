package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PrepareReceipt(ctx context.Context, event ReceiptEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var gatewayID string
	err = tx.QueryRow(ctx, `SELECT gateway_id FROM pending WHERE provider=$1 AND provider_id=$2`, event.Provider, event.ProviderID).Scan(&gatewayID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// All parts of a gateway share this lock; distinct provider IDs cannot
	// race aggregate final-state calculation. Matches outbox's messages->pending order.
	if _, err = tx.Exec(ctx, `SELECT gateway_id FROM messages WHERE gateway_id=$1 FOR UPDATE`, gatewayID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT `+pendingSelectColumns+` FROM pending WHERE gateway_id=$1 ORDER BY provider,provider_id FOR UPDATE`, gatewayID)
	if err != nil {
		return err
	}
	segments, err := pgx.CollectRows(rows, scanPending)
	if err != nil {
		return err
	}
	var rec Pending
	changed := false
	for i, p := range segments {
		if p.Provider == event.Provider && p.ProviderID == event.ProviderID {
			rec, changed = applyReceiptEvent(p, event)
			segments[i] = rec
			break
		}
	}
	if rec.ProviderID == "" {
		return ErrNotFound
	}
	if !changed {
		return tx.Commit(ctx)
	}
	if err = savePendingSQL(ctx, tx, rec); err != nil {
		return err
	}
	j := deliveryJob(rec, event, segments)
	agg := AggregateReceipt(segments)
	if agg.Final {
		if _, err = tx.Exec(ctx, `UPDATE messages SET state=$2,error_code=$3,done_at=$4 WHERE gateway_id=$1`, gatewayID, agg.State, agg.ErrorCode, event.DoneAt); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO receipt_jobs(id,kind,payload,next_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, j.ID, j.Kind, j.Payload, j.NextAt, j.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) MarkReceiptDelivered(ctx context.Context, rec Pending) error {
	_, err := s.pool.Exec(ctx, `UPDATE pending SET dlr_delivered=TRUE,dlr_ready=FALSE WHERE provider=$1 AND provider_id=$2 AND dlr_state=$3 AND dlr_done_at=$4`, rec.Provider, rec.ProviderID, rec.DLRState, rec.DLRDoneAt)
	return err
}

func (s *PostgresStore) BindMultipart(ctx context.Context, proposed MultipartBinding, part int, fingerprint string) (MultipartBinding, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MultipartBinding{}, err
	}
	defer tx.Rollback(ctx)
	// Also serializes the first insert when no row exists. Never held alongside
	// an admission/quota transaction, so it adds no quota lock-order dependency.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "multipart:"+proposed.Key); err != nil {
		return MultipartBinding{}, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM multipart_bindings WHERE key=$1`, proposed.Key).Scan(&raw)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MultipartBinding{}, err
	}
	var old MultipartBinding
	if found {
		if err = json.Unmarshal(raw, &old); err != nil {
			return MultipartBinding{}, err
		}
	}
	b, err := mergeMultipart(old, found, proposed, part, fingerprint, time.Now().UTC())
	if err != nil {
		return MultipartBinding{}, err
	}
	raw, err = json.Marshal(b)
	if err != nil {
		return MultipartBinding{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO multipart_bindings(key,payload,expires_at) VALUES($1,$2,$3)
ON CONFLICT(key) DO UPDATE SET payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at`, b.Key, raw, b.ExpiresAt); err != nil {
		return MultipartBinding{}, err
	}
	return b, tx.Commit(ctx)
}

func (s *PostgresStore) EnqueueReceipt(ctx context.Context, job ReceiptJob) error {
	job = normalizeJob(job)
	_, err := s.pool.Exec(ctx, `INSERT INTO receipt_jobs(id,kind,payload,next_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, job.ID, job.Kind, job.Payload, job.NextAt, job.ExpiresAt)
	return err
}

func (s *PostgresStore) ClaimReceipt(ctx context.Context, kind string, now time.Time) (ReceiptJob, bool, error) {
	var j ReceiptJob
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM receipt_jobs WHERE kind=$1 AND state IN ('pending','claimed')
 AND next_at <= $2 AND expires_at > $2 AND (state='pending' OR lease_until <= $2)
 ORDER BY next_at,id FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE receipt_jobs j SET state='claimed',attempt=j.attempt+1,lease=j.lease+1,lease_until=$2+INTERVAL '1 minute'
FROM candidate c WHERE j.id=c.id RETURNING j.id,j.kind,j.payload,j.state,j.attempt,j.lease,j.lease_until,j.next_at,j.expires_at,j.last_error`, kind, now).Scan(
		&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempt, &j.Lease, &j.LeaseUntil, &j.NextAt, &j.ExpiresAt, &j.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReceiptJob{}, false, nil
	}
	return j, err == nil, err
}

func (s *PostgresStore) FinishReceipt(ctx context.Context, job ReceiptJob, next time.Time, reason string) error {
	state := "done"
	if reason != "" {
		state = "pending"
		if next.IsZero() || !next.Before(job.ExpiresAt) || job.Attempt >= 1000 {
			state = "dead"
		}
	}
	if next.IsZero() {
		next = time.Now().UTC()
	}
	tag, err := s.pool.Exec(ctx, `UPDATE receipt_jobs SET state=$3,next_at=$4,last_error=$5,payload=CASE WHEN $3='done' THEN '{}'::jsonb ELSE payload END WHERE id=$1 AND lease=$2 AND state='claimed'`, job.ID, job.Lease, state, next, reason)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return err
}

func (s *PostgresStore) ReceiptCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind || ':' || state,COUNT(*) FROM receipt_jobs GROUP BY kind,state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err = rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		counts[key] = n
	}
	return counts, rows.Err()
}

func (s *PostgresStore) SweepReliability(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE receipt_jobs SET state='dead',last_error='delivery expired'
WHERE id IN (SELECT id FROM receipt_jobs WHERE expires_at<$1 AND
(state='pending' OR (state='claimed' AND lease_until<$1)) LIMIT 10000)`, now)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM receipt_jobs WHERE id IN (SELECT id FROM receipt_jobs WHERE expires_at<$1 OR (state='done' AND expires_at<$2) LIMIT 10000)`, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM multipart_bindings WHERE key IN (SELECT key FROM multipart_bindings WHERE expires_at<$1 LIMIT 10000)`, now.Add(-7*24*time.Hour))
	return err
}
