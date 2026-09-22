package httpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

// Exercises the production submit_sm encoder without a network connection or
// real SMS. Inspect Payload rather than the receive-side text heuristics.
type cedillaWireProvider struct {
	submits chan []smpp.SubmitSM
	calls   atomic.Int32
}

func (p *cedillaWireProvider) OnDLR(provider.DLRCallback) {}
func (p *cedillaWireProvider) Send(m provider.OutboundMessage) (string, error) {
	ids, err := p.SendAll(m)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}
func (p *cedillaWireProvider) SendAll(m provider.OutboundMessage) ([]string, error) {
	p.calls.Add(1)
	parts := smppclient.BuildSubmitSM(smppclient.Message{
		Text: m.Text, Encoding: m.Encoding, DataCoding: m.DataCoding,
		SourceAddr: m.SourceAddr, DestAddr: m.DestAddr,
	}, config.DefaultSMPPClientConfig())
	if len(parts) == 0 {
		return nil, fmt.Errorf("no encoded parts")
	}
	var submits []smpp.SubmitSM
	var ids []string
	for i, part := range parts {
		submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, Body: part.Body})
		if err != nil {
			return nil, err
		}
		submits = append(submits, submit)
		ids = append(ids, fmt.Sprintf("%s-part-%d", m.GatewayID, i+1))
	}
	p.submits <- submits
	return ids, nil
}

func TestHTTPCedillaGSM7Lifecycle(t *testing.T) {
	// Exact text from the reported HTTP request, including the trailing U+00E7.
	const reported = "@£$¥èéùìò ØøÅåΔ_ΦΓΛΩΠΨΣΘΞ^{}\\[~]|€ÆæßÉ\n\r!\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüàç"
	for _, tc := range []struct {
		name  string
		text  string
		parts int
	}{
		{"reported", reported, 1},
		{"single-boundary", strings.Repeat("ç", 160), 1},
		{"multipart-boundary", strings.Repeat("ç", 161), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := config.Default()
			enableClientAuth(&cfg)
			cfg.Risk = config.RiskConfig{PerNumberPerMinute: -1, PerNumberPerDay: -1, PerClientPerSecond: -1}
			cfg.Clients[0].TenantID = "cedilla-test"
			cfg.Tenants = []config.TenantConfig{{TenantID: "cedilla-test", Limits: config.TenantLimits{DailySegments: tc.parts + 1, Timezone: "UTC"}}}
			cfg.Providers = []config.ProviderConfig{{Name: "cedilla-test", Enabled: true}}
			cfg.Routes = []config.RouteConfig{{Name: "cedilla-route", Provider: "cedilla-test"}}
			p := &cedillaWireProvider{submits: make(chan []smpp.SubmitSM, 4)}
			reg := provider.NewRegistry()
			reg.Replace(map[string]provider.Provider{"cedilla-test": p})
			st := store.NewMemory()
			d := dispatch.New(nil, reg, nil, testDispatcherConfig(), st)
			defer d.Close()
			d.ReloadRoutes(cfg.Routes, cfg.Providers)
			g := NewWithDispatcher(cfg, st, d)
			post := func(key, text string, wantStatus int) dispatch.Receipt {
				t.Helper()
				body, err := json.Marshal(map[string]string{"from": "Brand", "to": "+8613800138000", "text": text, "client_msg_id": key})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				addClientAuth(req)
				rec := httptest.NewRecorder()
				g.Handler().ServeHTTP(rec, req)
				if rec.Code != wantStatus {
					t.Fatalf("submit %s: %d %s, want %d", key, rec.Code, rec.Body.String(), wantStatus)
				}
				var receipt dispatch.Receipt
				if wantStatus == http.StatusOK {
					if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil || receipt.GatewayID == "" {
						t.Fatalf("invalid receipt: %s err=%v", rec.Body.String(), err)
					}
				}
				return receipt
			}
			first := post("cedilla-order", tc.text, http.StatusOK)
			select {
			case submits := <-p.submits:
				if len(submits) != tc.parts {
					t.Fatalf("wire parts=%d want=%d", len(submits), tc.parts)
				}
				var wire []byte
				for i, submit := range submits {
					if submit.DataCoding != 0 {
						t.Fatalf("part %d DCS=%d, want 0", i+1, submit.DataCoding)
					}
					if tc.parts > 1 && (submit.Concat == nil || int(submit.Concat.Total) != tc.parts || int(submit.Concat.Part) != i+1) {
						t.Fatalf("invalid multipart metadata: %+v", submit.Concat)
					}
					wire = append(wire, submit.Payload...)
				}
				if len(wire) == 0 || wire[len(wire)-1] != 0x09 || !bytes.Equal(wire, message.EncodeGSM7Unpacked(tc.text)) {
					t.Fatalf("cedilla lost or payload changed: %x", wire)
				}
				t.Logf("characters=%d septets=%d DCS=0 parts=%d", len([]rune(tc.text)), len(wire), len(submits))
			case <-time.After(5 * time.Second):
				t.Fatal("no encoded upstream submit")
			}
			waitForPending(t, d, tc.parts)
			// One segment remains. A retry must not consume it; the next fresh
			// message uses it and only the following message exceeds the quota.
			if duplicate := post("cedilla-order", tc.text, http.StatusOK); duplicate.GatewayID != first.GatewayID {
				t.Fatal("idempotent gateway ID changed")
			}
			post("spare-segment", "ç", http.StatusOK)
			post("over-quota", "ç", http.StatusTooManyRequests)
			waitForPending(t, d, tc.parts+1)
			if p.calls.Load() != 2 {
				t.Fatalf("expected exactly two upstream sends, got %d", p.calls.Load())
			}
			// Deliver original message receipts after exhausting the daily quota.
			for i := tc.parts; i >= 1; i-- {
				if err := d.EnqueueDLR(ctx, provider.DLR{Provider: "cedilla-test", ProviderID: fmt.Sprintf("%s-part-%d", first.GatewayID, i), State: "DELIVRD", DoneAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				counts, err := st.ReceiptCounts(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if counts["delivery:done"] == tc.parts {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("receipts incomplete: %+v", counts)
				}
				time.Sleep(10 * time.Millisecond)
			}
			stored, ok, err := st.GetMessage(ctx, first.GatewayID)
			if err != nil || !ok || stored.Text != tc.text || stored.Encoding != "gsm7" || len(stored.Segments) != tc.parts || stored.State != "DELIVRD" {
				t.Fatalf("stored message or DR state changed: %+v err=%v", stored, err)
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
			addClientAuth(req)
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, req)
			var listed []message.Message
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 2 {
				t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
			}
			found := false
			for _, msg := range listed {
				if msg.ID == first.GatewayID {
					found = true
					if msg.Text != tc.text || msg.Encoding != "gsm7" || msg.State != "DELIVRD" {
						t.Fatalf("query lost original text or status: %+v", msg)
					}
				}
			}
			if !found {
				t.Fatal("original message missing from query")
			}
		})
	}
}
