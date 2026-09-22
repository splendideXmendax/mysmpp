package httpgw

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type tenantSubmitResponse struct {
	GatewayID string `json:"gateway_id"`
	State     string `json:"state"`
}

// Keep the existing query field names and values, but do not serialize internal
// message entities directly to tenants. Admin and persistence formats stay intact.
type tenantMessageResponse struct {
	ID          string
	TenantID    string
	AccountID   string
	ClientMsgID string
	Direction   message.Direction
	From        string
	To          string
	Text        string
	Encoding    string
	SourceKind  string
	SourceID    string
	State       string
	ErrorCode   int
	Segments    []message.Segment
	Metadata    map[string]string
	SubmittedAt time.Time
	SentAt      time.Time
	DoneAt      time.Time
}

func tenantMessage(msg message.Message) tenantMessageResponse {
	return tenantMessageResponse{
		ID: msg.ID, TenantID: msg.TenantID, AccountID: msg.AccountID, ClientMsgID: msg.ClientMsgID,
		Direction: msg.Direction, From: msg.From, To: msg.To, Text: msg.Text, Encoding: msg.Encoding,
		SourceKind: msg.SourceKind, SourceID: msg.SourceID, State: msg.State, ErrorCode: msg.ErrorCode,
		Segments: msg.Segments, Metadata: msg.Metadata,
		SubmittedAt: msg.SubmittedAt, SentAt: msg.SentAt, DoneAt: msg.DoneAt,
	}
}

func tenantMessages(messages []message.Message) []tenantMessageResponse {
	if messages == nil {
		return nil
	}
	result := make([]tenantMessageResponse, len(messages))
	for i, msg := range messages {
		result[i] = tenantMessage(msg)
	}
	return result
}

// Only the authenticated context selects tenant output, never a raw request
// header. Detailed errors remain available to operators, not API clients.
func writeMessageError(w http.ResponseWriter, r *http.Request, err error, status int, tenantText string) {
	text := err.Error()
	if clientID := clientIDFromRequest(r); clientID != "" {
		text = tenantText
		if status >= 500 {
			slog.Error("message API request failed", "method", r.Method, "client_id", clientID, "err", err)
		}
	}
	http.Error(w, text, status)
}

func submitErrorResponse(err error) (int, string) {
	// Preserve the existing HTTP status mapping while dropping wrapped internal
	// details such as route names, provider endpoints and database diagnostics.
	for _, item := range []struct {
		err    error
		status int
	}{
		{dispatch.ErrRateExceeded, http.StatusTooManyRequests},
		{dispatch.ErrQuotaExceeded, http.StatusTooManyRequests},
		{dispatch.ErrTenantDisabled, http.StatusForbidden},
		{dispatch.ErrBlocked, http.StatusForbidden},
		{dispatch.ErrInvalidDestAddr, http.StatusBadRequest},
		{dispatch.ErrInvalidMessageLength, http.StatusBadRequest},
		{dispatch.ErrNoRoute, http.StatusBadGateway},
	} {
		if errors.Is(err, item.err) {
			return item.status, item.err.Error()
		}
	}
	return http.StatusBadGateway, "message submission failed"
}
