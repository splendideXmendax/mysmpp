package httpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

// Captures actual submit_sm bodies built by the production encoder. No external
// SMS is sent, and checking bytes catches truncation at NUL independently of DR.
type asciiWireProvider struct {
	wire  chan []byte
	calls atomic.Int32
}

func (p *asciiWireProvider) OnDLR(provider.DLRCallback) {}
func (p *asciiWireProvider) Send(m provider.OutboundMessage) (string, error) {
	ids, err := p.SendAll(m)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}
func (p *asciiWireProvider) SendAll(m provider.OutboundMessage) ([]string, error) {
	p.calls.Add(1)
	parts := smppclient.BuildSubmitSM(smppclient.Message{Text: m.Text, Encoding: m.Encoding, DataCoding: m.DataCoding, SourceAddr: m.SourceAddr, DestAddr: m.DestAddr}, config.DefaultSMPPClientConfig())
	var wire []byte
	var ids []string
	for i, part := range parts {
		submit, err := smpp.ParseSubmitSM(smpp.PDU{CommandID: smpp.CommandSubmitSM, Body: part.Body})
		if err != nil {
			return nil, err
		}
		if submit.DataCoding != 8 {
			return nil, fmt.Errorf("unexpected DCS %d", submit.DataCoding)
		}
		wire = append(wire, submit.Payload...)
		ids = append(ids, fmt.Sprintf("ascii-part-%d", i+1))
	}
	p.wire <- wire
	return ids, nil
}

func TestHTTPASCIILosslessLifecycle(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testHTTPASCIILifecycle(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("MYSMPP_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("MYSMPP_TEST_POSTGRES_DSN is not set")
		}
		ctx := context.Background()
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		schema := fmt.Sprintf("mysmpp_http_ascii_%d", time.Now().UnixNano())
		if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			query := u.Query()
			query.Set("search_path", schema)
			u.RawQuery = query.Encode()
			dsn = u.String()
		} else {
			dsn += " search_path=" + schema
		}
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("migrations: %v", err)
		}
		sort.Strings(paths)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, string(data)); err != nil {
				t.Fatal(err)
			}
		}
		st, err := store.NewPostgres(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		testHTTPASCIILifecycle(t, st)
	})
}

func testHTTPASCIILifecycle(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	ascii := make([]byte, 128)
	for i := range ascii {
		ascii[i] = byte(i)
	}
	cfg := config.Default()
	enableClientAuth(&cfg)
	cfg.Risk = config.RiskConfig{PerNumberPerMinute: -1, PerNumberPerDay: -1, PerClientPerSecond: -1}
	cfg.Clients[0].TenantID = "ascii-test"
	cfg.Tenants = []config.TenantConfig{{TenantID: "ascii-test", Limits: config.TenantLimits{DailySegments: 2, Timezone: "UTC"}}}
	cfg.Providers = []config.ProviderConfig{{Name: "ascii-test", Enabled: true}}
	cfg.Routes = []config.RouteConfig{{Name: "ascii-test", Provider: "ascii-test"}}
	p := &asciiWireProvider{wire: make(chan []byte, 4)}
	reg := provider.NewRegistry()
	reg.Replace(map[string]provider.Provider{"ascii-test": p})
	d := dispatch.New(nil, reg, nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes(cfg.Routes, cfg.Providers)
	g := NewWithDispatcher(cfg, st, d)
	var gatewayID string
	for i, key := range []string{"ascii-order", "ascii-order", "over-quota"} {
		body, err := json.Marshal(map[string]string{"from": "brand", "to": "+8613800138000", "text": string(ascii), "client_msg_id": key})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		addClientAuth(req)
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, req)
		if i == 2 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("quota not applied: %d %s", rec.Code, rec.Body.String())
			}
			continue
		}
		var receipt dispatch.Receipt
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &receipt) != nil || receipt.GatewayID == "" {
			t.Fatalf("submit rejected: %d %s", rec.Code, rec.Body.String())
		}
		if i == 1 && receipt.GatewayID != gatewayID {
			t.Fatal("idempotency changed")
		}
		gatewayID = receipt.GatewayID
	}
	select {
	case wire := <-p.wire:
		if !bytes.Equal(wire, message.EncodeText(string(ascii), 8)) {
			t.Fatalf("upstream body changed: got %x", wire)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no upstream submit")
	}
	for i := 1; i <= 2; i++ {
		if err := d.EnqueueDLR(ctx, provider.DLR{Provider: "ascii-test", ProviderID: fmt.Sprintf("ascii-part-%d", i), State: "DELIVRD", DoneAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		counts, err := st.ReceiptCounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if counts["delivery:done"] == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipts incomplete: %+v", counts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("duplicate upstream send: %d", p.calls.Load())
	}
	msg, ok, err := st.GetMessage(ctx, gatewayID)
	if err != nil || !ok || msg.Text != string(ascii) || msg.State != "DELIVRD" {
		t.Fatalf("final message mismatch: %+v %v", msg, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	addClientAuth(req)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	var listed []message.Message
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].Text != string(ascii) {
		t.Fatalf("HTTP query changed text: %d %s", rec.Code, rec.Body.String())
	}
}
