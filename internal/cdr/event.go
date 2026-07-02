package cdr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type Event struct {
	Seq        uint64 `json:"seq"`
	Ts         string `json:"ts"`
	Kind       string `json:"kind"`
	GatewayID  string `json:"gateway_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	TextLen    int    `json:"text_len,omitempty"`
	TextHash   string `json:"text_hash,omitempty"`
	Text       string `json:"text,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
	Segments   int    `json:"segments,omitempty"`
	Route      string `json:"route,omitempty"`
	Provider   string `json:"provider,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SystemID   string `json:"system_id,omitempty"`
	Source     string `json:"source,omitempty"`
	State      string `json:"state,omitempty"`
	ErrorCode  int    `json:"error_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	FilterRule string `json:"filter_rule,omitempty"`
	Instance   string `json:"instance,omitempty"`
}

func (e *Event) normalize(instance string, maskTo, storeText bool) {
	if e.Ts == "" {
		e.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if instance != "" && e.Instance == "" {
		e.Instance = instance
	}
	if maskTo {
		e.To = maskMSISDN(e.To)
	}
	if !storeText {
		e.Text = ""
	}
}

func TextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func maskMSISDN(value string) string {
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return strings.Repeat("*", len(value)-4) + value[len(value)-4:]
}
