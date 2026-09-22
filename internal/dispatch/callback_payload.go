package dispatch

import (
	"time"

	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

// httpCallbackPayload is the tenant-facing allowlist. Internal receipt snapshots
// retain provider and route data for matching, retries and operator diagnostics.
type httpCallbackPayload struct {
	DeliveryID   string `json:"delivery_id"`
	GatewayID    string `json:"gateway_id"`
	ClientMsgID  string `json:"client_msg_id"`
	SegmentCount int    `json:"segment_count"`
	State        string `json:"state"`
	MessageState string `json:"message_state"`
	Final        bool   `json:"final"`
	ErrorCode    int    `json:"error_code"`
	DoneAt       string `json:"done_at"`
	CallbackRule string `json:"callback_rule,omitempty"`
	SubmitStatus uint32 `json:"submit_status,omitempty"`
}

func newHTTPCallbackPayload(rec store.Pending, dlr provider.DLR, aggregate dlrAggregate) httpCallbackPayload {
	return httpCallbackPayload{
		DeliveryID: httpFinalReceiptID(rec.GatewayID),
		GatewayID:  rec.GatewayID, ClientMsgID: rec.ClientMsgID,
		SegmentCount: max(rec.SegmentCount, 1),
		State:        aggregate.State, MessageState: aggregate.State, Final: aggregate.Final,
		ErrorCode: aggregate.ErrorCode, DoneAt: dlr.DoneAt.Format(time.RFC3339Nano),
		CallbackRule: rec.CallbackRule, SubmitStatus: rec.UpstreamStatus,
	}
}

func httpFinalReceiptID(gatewayID string) string {
	// Domain separated from per-part events; independent of provider IDs and
	// arrival order. Gateway IDs are globally unique across tenants.
	return store.ReceiptKey("http-final", store.ReceiptEvent{ProviderID: gatewayID})
}
