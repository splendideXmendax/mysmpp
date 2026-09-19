package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

// postgresText keeps ordinary text readable in SQL and uses BYTEA only when
// PostgreSQL TEXT cannot represent it. The original string is never sanitized.
func postgresText(text string) (string, []byte) {
	if strings.IndexByte(text, 0) >= 0 {
		return "", []byte(text)
	}
	return text, nil
}

// These envelopes belong only to the PostgreSQL persistence boundary. Public
// JSON, file storage, routing, encoding and quota calculation use original text.
type postgresOutboxPayload struct {
	OutboxPayload
	TextBytes []byte `json:"text_bytes,omitempty"`
}

func marshalPostgresOutbox(p OutboxPayload) ([]byte, error) {
	text, raw := postgresText(p.Text)
	if raw == nil {
		return json.Marshal(p)
	}
	p.Text = text
	return json.Marshal(postgresOutboxPayload{OutboxPayload: p, TextBytes: raw})
}

func unmarshalPostgresOutbox(raw []byte) (OutboxPayload, error) {
	var p postgresOutboxPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return OutboxPayload{}, err
	}
	if p.TextBytes != nil {
		p.Text = string(p.TextBytes)
	}
	return p.OutboxPayload, nil
}

type postgresReceiptDelivery struct {
	ReceiptDelivery
	TextBytes []byte `json:"text_bytes,omitempty"`
}

func marshalPostgresReceipt(j ReceiptJob) ([]byte, error) {
	// No decoding/re-encoding on the ordinary receipt path. A literal backslash
	// sequence may match this check, but only a decoded NUL enables the envelope.
	if j.Kind != "delivery" || !bytes.Contains(j.Payload, []byte(`\u0000`)) {
		return j.Payload, nil
	}
	var d ReceiptDelivery
	if err := json.Unmarshal(j.Payload, &d); err != nil {
		return nil, err
	}
	text, raw := postgresText(d.Pending.Text)
	if raw == nil {
		return j.Payload, nil
	}
	d.Pending.Text = text
	return json.Marshal(postgresReceiptDelivery{ReceiptDelivery: d, TextBytes: raw})
}

func unmarshalPostgresReceipt(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte(`"text_bytes"`)) {
		return raw, nil
	}
	var d postgresReceiptDelivery
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.TextBytes == nil {
		return raw, nil
	}
	d.Pending.Text = string(d.TextBytes)
	return json.Marshal(d.ReceiptDelivery)
}

func insertReceiptSQL(ctx context.Context, exec sqlExecer, j ReceiptJob) error {
	payload, err := marshalPostgresReceipt(j)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, `INSERT INTO receipt_jobs(id,kind,payload,next_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, j.ID, j.Kind, payload, j.NextAt, j.ExpiresAt)
	return err
}
