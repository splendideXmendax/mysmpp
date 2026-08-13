package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func NewPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	if dsn == "" {
		return nil, errors.New("postgres storage.dsn is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 50
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	st := &PostgresStore{pool: pool}
	if err := st.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := st.EnsureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return st, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) SaveMessage(ctx context.Context, msg message.Message) error {
	return saveMessageSQL(ctx, s.pool, msg)
}

func saveMessageSQL(ctx context.Context, exec sqlExecer, msg message.Message) error {
	meta, err := json.Marshal(msg.Metadata)
	if err != nil {
		return err
	}
	segments := len(msg.Segments)
	if segments == 0 {
		segments = 1
	}
	_, err = exec.Exec(ctx, `
INSERT INTO messages (
	gateway_id, provider_id, direction, from_addr, to_addr, text, encoding, data_coding,
	segments, route, provider, source_kind, source_session, client_id, state, error_code,
	received_at, sent_at, done_at, meta
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8,
	$9, $10, $11, $12, $13, $14, $15, $16,
	$17, $18, $19, $20
)
ON CONFLICT (gateway_id) DO UPDATE SET
	provider_id = EXCLUDED.provider_id,
	direction = EXCLUDED.direction,
	from_addr = EXCLUDED.from_addr,
	to_addr = EXCLUDED.to_addr,
	text = EXCLUDED.text,
	encoding = EXCLUDED.encoding,
	data_coding = EXCLUDED.data_coding,
	segments = EXCLUDED.segments,
	route = EXCLUDED.route,
	provider = EXCLUDED.provider,
	source_kind = EXCLUDED.source_kind,
	source_session = EXCLUDED.source_session,
	client_id = EXCLUDED.client_id,
	state = EXCLUDED.state,
	error_code = EXCLUDED.error_code,
	received_at = EXCLUDED.received_at,
	sent_at = EXCLUDED.sent_at,
	done_at = EXCLUDED.done_at,
	meta = EXCLUDED.meta`,
		msg.ID, nullString(msg.ProviderID), string(msg.Direction), msg.From, msg.To, msg.Text, nullString(msg.Encoding), dataCodingFromEncoding(msg.Encoding),
		segments, nullString(msg.Route), nullString(msg.Provider), nullString(msg.SourceKind), nullString(msg.SourceID), nullString(msg.Metadata["client_id"]), msg.State, msg.ErrorCode,
		zeroAsNow(msg.SubmittedAt), nullTime(msg.SentAt), nullTime(msg.DoneAt), meta,
	)
	return err
}

func (s *PostgresStore) GetMessage(ctx context.Context, gatewayID string) (message.Message, bool, error) {
	rows, err := s.pool.Query(ctx, `
SELECT gateway_id, COALESCE(provider_id, ''), direction, from_addr, to_addr, text, COALESCE(encoding, ''),
	COALESCE(route, ''), COALESCE(provider, ''), COALESCE(source_kind, ''), COALESCE(source_session, ''),
	state, COALESCE(error_code, 0), received_at, sent_at, done_at, COALESCE(meta, '{}'::jsonb)
FROM messages WHERE gateway_id = $1`, gatewayID)
	if err != nil {
		return message.Message{}, false, err
	}
	msgs, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		return message.Message{}, false, err
	}
	if len(msgs) == 0 {
		return message.Message{}, false, nil
	}
	return msgs[0], true, nil
}

func (s *PostgresStore) UpdateMessageSent(ctx context.Context, gatewayID, providerID string) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE messages SET provider_id = $2, state = 'sent', sent_at = NOW()
WHERE gateway_id = $1`, gatewayID, providerID)
	return checkRows(tag, err)
}

func (s *PostgresStore) UpdateMessageState(ctx context.Context, gatewayID, state string, errCode int) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE messages SET state = $2, error_code = $3, done_at = NOW()
WHERE gateway_id = $1`, gatewayID, state, errCode)
	return checkRows(tag, err)
}

func (s *PostgresStore) ListMessages(ctx context.Context) ([]message.Message, error) {
	return s.ListMessagesPage(ctx, ListOptions{Limit: 100})
}

func (s *PostgresStore) ListMessagesPage(ctx context.Context, opts ListOptions) ([]message.Message, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	query := `
SELECT gateway_id, COALESCE(provider_id, ''), direction, from_addr, to_addr, text, COALESCE(encoding, ''),
	COALESCE(route, ''), COALESCE(provider, ''), COALESCE(source_kind, ''), COALESCE(source_session, ''),
	state, COALESCE(error_code, 0), received_at, sent_at, done_at, COALESCE(meta, '{}'::jsonb)
FROM messages`
	args := []any{limit, offset}
	if opts.ClientID != "" {
		query += ` WHERE client_id = $3`
		args = append(args, opts.ClientID)
	}
	query += ` ORDER BY received_at ASC, gateway_id ASC LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMessage)
}

func (s *PostgresStore) SavePending(ctx context.Context, p Pending) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO pending (
	provider_id, gateway_id, source_kind, source_session, source_system, from_addr, to_addr,
	text, data_coding, registered_delivery, provider, route, received_at, expires_at,
	dlr_ready, dlr_state, dlr_err, dlr_done_at, callback_url, callback_rule
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT (provider_id) DO UPDATE SET
	gateway_id = EXCLUDED.gateway_id,
	source_kind = EXCLUDED.source_kind,
	source_session = EXCLUDED.source_session,
	source_system = EXCLUDED.source_system,
	callback_url = EXCLUDED.callback_url,
	callback_rule = EXCLUDED.callback_rule,
	from_addr = EXCLUDED.from_addr,
	to_addr = EXCLUDED.to_addr,
	text = EXCLUDED.text,
	data_coding = EXCLUDED.data_coding,
	registered_delivery = EXCLUDED.registered_delivery,
	provider = EXCLUDED.provider,
	route = EXCLUDED.route,
	received_at = EXCLUDED.received_at,
	expires_at = EXCLUDED.expires_at,
	dlr_ready = EXCLUDED.dlr_ready,
	dlr_state = EXCLUDED.dlr_state,
	dlr_err = EXCLUDED.dlr_err,
	dlr_done_at = EXCLUDED.dlr_done_at`,
		p.ProviderID, p.GatewayID, p.SourceKind, nullString(p.SourceSession), nullString(p.SourceSystem), nullString(p.From), nullString(p.To),
		nullString(p.Text), p.DataCoding, p.RegisteredDelivery, nullString(p.Provider), nullString(p.Route), zeroAsNow(p.ReceivedAt), p.ExpiresAt,
		p.DLRReady, nullString(p.DLRState), p.DLRErrorCode, nullTime(p.DLRDoneAt), nullString(p.CallbackURL), nullString(p.CallbackRule),
	)
	return err
}

func (s *PostgresStore) GetPending(ctx context.Context, providerID string) (Pending, bool, error) {
	rows, err := s.pool.Query(ctx, `
SELECT provider_id, gateway_id, source_kind, COALESCE(source_session, ''), COALESCE(source_system, ''),
	COALESCE(callback_url, ''), COALESCE(callback_rule, ''),
	COALESCE(from_addr, ''), COALESCE(to_addr, ''), COALESCE(text, ''), COALESCE(data_coding, 0),
	COALESCE(registered_delivery, 0), COALESCE(provider, ''), COALESCE(route, ''), received_at, expires_at,
	COALESCE(dlr_ready, FALSE), COALESCE(dlr_state, ''), COALESCE(dlr_err, 0), dlr_done_at
FROM pending WHERE provider_id = $1`, providerID)
	if err != nil {
		return Pending{}, false, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Pending, error) {
		var p Pending
		var dataCoding, registeredDelivery int16
		var doneAt *time.Time
		err := row.Scan(&p.ProviderID, &p.GatewayID, &p.SourceKind, &p.SourceSession, &p.SourceSystem, &p.CallbackURL, &p.CallbackRule, &p.From, &p.To, &p.Text, &dataCoding,
			&registeredDelivery, &p.Provider, &p.Route, &p.ReceivedAt, &p.ExpiresAt, &p.DLRReady, &p.DLRState, &p.DLRErrorCode, &doneAt)
		p.DataCoding = uint8(dataCoding)
		p.RegisteredDelivery = uint8(registeredDelivery)
		if doneAt != nil {
			p.DLRDoneAt = *doneAt
		}
		return p, err
	})
	if err != nil {
		return Pending{}, false, err
	}
	if len(items) == 0 {
		return Pending{}, false, nil
	}
	return items[0], true, nil
}

func (s *PostgresStore) MarkDLRReady(ctx context.Context, providerID, state string, errCode int, doneAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE pending SET dlr_ready = TRUE, dlr_state = $2, dlr_err = $3, dlr_done_at = $4
WHERE provider_id = $1`, providerID, state, errCode, zeroAsNow(doneAt))
	return checkRows(tag, err)
}

func (s *PostgresStore) ListReadyDLR(ctx context.Context, systemID string, limit int) ([]Pending, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT provider_id, gateway_id, source_kind, COALESCE(source_session, ''), COALESCE(source_system, ''),
	COALESCE(callback_url, ''), COALESCE(callback_rule, ''),
	COALESCE(from_addr, ''), COALESCE(to_addr, ''), COALESCE(text, ''), COALESCE(data_coding, 0),
	COALESCE(registered_delivery, 0), COALESCE(provider, ''), COALESCE(route, ''), received_at, expires_at,
	COALESCE(dlr_ready, FALSE), COALESCE(dlr_state, ''), COALESCE(dlr_err, 0), dlr_done_at
FROM pending
WHERE dlr_ready = TRUE AND ($1 = '' OR source_system = $1)
ORDER BY received_at ASC, provider_id ASC
LIMIT $2`, systemID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPending)
}

func (s *PostgresStore) DeletePending(ctx context.Context, providerID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pending WHERE provider_id = $1`, providerID)
	return err
}

func (s *PostgresStore) SweepExpiredPending(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM pending WHERE provider_id IN (
	SELECT provider_id FROM pending WHERE dlr_ready = FALSE AND expires_at < $1 ORDER BY expires_at LIMIT 10000
)`, before)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) PendingSize(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM pending WHERE dlr_ready = TRUE OR expires_at >= NOW()`).Scan(&n)
	return n, err
}

func (s *PostgresStore) ReserveGatewayIDRange(ctx context.Context, span uint64) (uint64, uint64, error) {
	if span == 0 {
		span = 1
	}
	if span > 1<<63-1 {
		return 0, 0, fmt.Errorf("id allocation span too large: %d", span)
	}
	var end int64
	err := s.pool.QueryRow(ctx, `
INSERT INTO id_alloc (name, value) VALUES ('gateway_id', $1)
ON CONFLICT (name) DO UPDATE SET value = id_alloc.value + EXCLUDED.value
RETURNING value`, int64(span)).Scan(&end)
	if err != nil {
		return 0, 0, err
	}
	if end < 0 {
		return 0, 0, fmt.Errorf("id allocation high water is negative: %d", end)
	}
	endU := uint64(end)
	return endU - span + 1, endU, nil
}

func (s *PostgresStore) EnqueueOutbox(ctx context.Context, item OutboxItem) (int64, error) {
	if item.State == "" {
		item.State = "pending"
	}
	if item.NextRetryAt.IsZero() {
		item.NextRetryAt = time.Now().UTC()
	}
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = 5
	}
	payload, err := json.Marshal(item.Payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
INSERT INTO outbox (gateway_id, provider, payload, state, next_retry_at, max_attempts)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`, item.GatewayID, item.Provider, payload, item.State, item.NextRetryAt, item.MaxAttempts).Scan(&id)
	return id, err
}

func (s *PostgresStore) ClaimOutbox(ctx context.Context, workerID string, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.pool.Query(ctx, `
UPDATE outbox SET state = 'claimed', claimed_by = $1, claimed_at = NOW(), attempt = attempt + 1
WHERE id IN (
	SELECT id FROM (
		SELECT id FROM outbox
		WHERE state = 'pending' AND next_retry_at <= NOW()
		ORDER BY next_retry_at, id
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	) claimable
)
RETURNING id, gateway_id, provider, payload, state, COALESCE(claimed_by, ''), claimed_at,
	next_retry_at, attempt, max_attempts, COALESCE(last_error, ''), created_at`, workerID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanOutbox)
}

func (s *PostgresStore) RequeueStaleOutbox(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE outbox
SET state = 'pending', claimed_by = NULL, claimed_at = NULL, next_retry_at = LEAST(next_retry_at, NOW())
WHERE id IN (
	SELECT id FROM outbox
	WHERE state = 'claimed' AND claimed_at < $1
	ORDER BY claimed_at, id
	LIMIT $2
	FOR UPDATE SKIP LOCKED
)`, before, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) AckOutbox(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE outbox
	SET state = 'done',
	    payload = payload - ARRAY[
	        'udh',
	        'raw_payload', 'raw_payload_set',
        'sar_ref_num', 'sar_total_segments', 'sar_segment_seqnum', 'sar_set'
    ]
WHERE id = $1`, id)
	return checkRows(tag, err)
}

func (s *PostgresStore) FailOutbox(ctx context.Context, id int64, errMsg string, nextRetryAt time.Time) error {
	state := "pending"
	var retry any = nextRetryAt
	if nextRetryAt.IsZero() {
		state = "failed"
		retry = nil
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE outbox SET state = $2, last_error = $3, next_retry_at = COALESCE($4, next_retry_at),
	claimed_by = NULL, claimed_at = NULL
WHERE id = $1`, id, state, errMsg, retry)
	return checkRows(tag, err)
}

func (s *PostgresStore) OutboxDepth(ctx context.Context, state string) (int, error) {
	var n int
	var err error
	if state == "" {
		err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE state = $1`, state).Scan(&n)
	}
	return n, err
}

func (s *PostgresStore) CheckIdempotency(ctx context.Context, clientID, key string) (string, bool, error) {
	if clientID == "" || key == "" {
		return "", false, nil
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM idempotency WHERE expires_at < NOW()`)
	rows, err := s.pool.Query(ctx, `
SELECT gateway_id FROM idempotency
WHERE client_id = $1 AND key = $2 AND expires_at >= NOW()`, clientID, key)
	if err != nil {
		return "", false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", false, err
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	return ids[0], true, nil
}

func (s *PostgresStore) SaveIdempotency(ctx context.Context, clientID, key, gatewayID string, ttl time.Duration) error {
	if clientID == "" || key == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO idempotency (client_id, key, gateway_id, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (client_id, key) DO UPDATE SET
	gateway_id = EXCLUDED.gateway_id,
	expires_at = EXCLUDED.expires_at`, clientID, key, gatewayID, time.Now().UTC().Add(ttl))
	return err
}

func (s *PostgresStore) SubmitAtomic(ctx context.Context, msg message.Message, item OutboxItem, clientID, key string, ttl time.Duration) (int64, string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", false, err
	}
	defer tx.Rollback(ctx)

	if clientID != "" && key != "" {
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		tag, err := tx.Exec(ctx, `DELETE FROM idempotency WHERE client_id = $1 AND key = $2 AND expires_at < NOW()`, clientID, key)
		if err != nil {
			return 0, "", false, err
		}
		_ = tag
		tag, err = tx.Exec(ctx, `
INSERT INTO idempotency (client_id, key, gateway_id, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (client_id, key) DO NOTHING`, clientID, key, msg.ID, time.Now().UTC().Add(ttl))
		if err != nil {
			return 0, "", false, err
		}
		if tag.RowsAffected() == 0 {
			var existing string
			if err := tx.QueryRow(ctx, `
SELECT gateway_id FROM idempotency
WHERE client_id = $1 AND key = $2 AND expires_at >= NOW()`, clientID, key).Scan(&existing); err != nil {
				return 0, "", false, err
			}
			return 0, existing, true, nil
		}
	}

	if err := saveMessageSQL(ctx, tx, msg); err != nil {
		return 0, "", false, err
	}
	if item.State == "" {
		item.State = "pending"
	}
	if item.NextRetryAt.IsZero() {
		item.NextRetryAt = time.Now().UTC()
	}
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = 5
	}
	payload, err := json.Marshal(item.Payload)
	if err != nil {
		return 0, "", false, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
INSERT INTO outbox (gateway_id, provider, payload, state, next_retry_at, max_attempts)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`, item.GatewayID, item.Provider, payload, item.State, item.NextRetryAt, item.MaxAttempts).Scan(&id); err != nil {
		return 0, "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", false, err
	}
	return id, msg.ID, false, nil
}

func scanMessage(row pgx.CollectableRow) (message.Message, error) {
	var msg message.Message
	var direction string
	var meta []byte
	var sentAt, doneAt *time.Time
	err := row.Scan(&msg.ID, &msg.ProviderID, &direction, &msg.From, &msg.To, &msg.Text, &msg.Encoding,
		&msg.Route, &msg.Provider, &msg.SourceKind, &msg.SourceID, &msg.State, &msg.ErrorCode,
		&msg.SubmittedAt, &sentAt, &doneAt, &meta)
	if err != nil {
		return message.Message{}, err
	}
	msg.Direction = message.Direction(direction)
	if sentAt != nil {
		msg.SentAt = *sentAt
	}
	if doneAt != nil {
		msg.DoneAt = *doneAt
	}
	msg.Metadata = map[string]string{}
	_ = json.Unmarshal(meta, &msg.Metadata)
	msg.Segments = message.Split(msg.Text, message.SplitOptions{ForceEncoding: msg.Encoding})
	return msg, nil
}

func scanOutbox(row pgx.CollectableRow) (OutboxItem, error) {
	var item OutboxItem
	var payload []byte
	err := row.Scan(&item.ID, &item.GatewayID, &item.Provider, &payload, &item.State, &item.ClaimedBy, &item.ClaimedAt,
		&item.NextRetryAt, &item.Attempt, &item.MaxAttempts, &item.LastError, &item.CreatedAt)
	if err != nil {
		return OutboxItem{}, err
	}
	if err := json.Unmarshal(payload, &item.Payload); err != nil {
		return OutboxItem{}, err
	}
	return item, nil
}

func scanPending(row pgx.CollectableRow) (Pending, error) {
	var p Pending
	var dataCoding, registeredDelivery int16
	var doneAt *time.Time
	err := row.Scan(&p.ProviderID, &p.GatewayID, &p.SourceKind, &p.SourceSession, &p.SourceSystem, &p.CallbackURL, &p.CallbackRule, &p.From, &p.To, &p.Text, &dataCoding,
		&registeredDelivery, &p.Provider, &p.Route, &p.ReceivedAt, &p.ExpiresAt, &p.DLRReady, &p.DLRState, &p.DLRErrorCode, &doneAt)
	if err != nil {
		return Pending{}, err
	}
	p.DataCoding = uint8(dataCoding)
	p.RegisteredDelivery = uint8(registeredDelivery)
	if doneAt != nil {
		p.DLRDoneAt = *doneAt
	}
	return p, nil
}

func checkRows(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func zeroAsNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}

func dataCodingFromEncoding(encoding string) int {
	if encoding == "ucs2" {
		return 8
	}
	return 0
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if _, err := s.OutboxDepth(ctx, "pending"); err != nil {
		if isUndefinedTable(err) {
			return fmt.Errorf("postgres schema is missing; run migrations before startup: %w", err)
		}
		return err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO id_alloc (name, value) VALUES ('gateway_id', 0) ON CONFLICT (name) DO NOTHING`); err != nil {
		if isUndefinedTable(err) {
			return fmt.Errorf("postgres id_alloc table is missing; run migrations before startup: %w", err)
		}
		return err
	}
	if _, err := s.pool.Exec(ctx, `SELECT dlr_ready, dlr_state, dlr_err, dlr_done_at, callback_url, callback_rule FROM pending LIMIT 0`); err != nil {
		return fmt.Errorf("postgres pending DLR columns are missing; run migrations before startup: %w", err)
	}
	return nil
}
