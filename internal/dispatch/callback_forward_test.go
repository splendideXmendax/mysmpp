package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type forwardTestTransport func(*http.Request) (*http.Response, error)

func (f forwardTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallbackForwardingTenantAndExactURL(t *testing.T) {
	rule := config.CallbackForwarding{TenantID: "test-c", CallbackURL: "http://callbacks.example:2761/sms/dr", TargetURL: "http://10.20.30.40:2761/sms/dr"}
	var direct, forwarded atomic.Int32
	d := &Dispatcher{logger: slog.Default(), httpClient: &http.Client{Transport: forwardTestTransport(func(r *http.Request) (*http.Response, error) {
		direct.Add(1)
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})}}
	d.ReloadCallbacks(config.DispatcherConfig{CallbackForwarding: []config.CallbackForwarding{rule}})
	table := d.callbackForwards.Load()
	defer table.closeIdle()
	table.routes[callbackForwardKey{rule.TenantID, rule.CallbackURL}].client.Transport = forwardTestTransport(func(r *http.Request) (*http.Response, error) {
		forwarded.Add(1)
		if r.URL.String() != rule.TargetURL || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected forward: %s %s", r.Method, r.URL)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["gateway_id"] != "gateway-1" || payload["client_msg_id"] != "order-1" || payload["segment_count"] != float64(3) || payload["final"] != true {
			t.Errorf("changed callback payload: %+v err=%v", payload, err)
		}
		if r.Header.Get("X-Token") != "" || r.Header.Get("Authorization") != "" {
			t.Error("forward leaked customer credentials")
		}
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})
	for _, tc := range []struct{ tenant, url string }{
		{rule.TenantID, rule.CallbackURL},
		{"other-tenant", rule.CallbackURL},
		{"", rule.CallbackURL},
		{rule.TenantID, rule.CallbackURL + "/extra"},
		{rule.TenantID, rule.CallbackURL + "?target=other"},
		{rule.TenantID, strings.Replace(rule.CallbackURL, "http:", "https:", 1)},
		{rule.TenantID, "http://callbacks.example.evil:2761/sms/dr"},
	} {
		rec := store.Pending{TenantID: tc.tenant, CallbackURL: tc.url, GatewayID: "gateway-1", ClientMsgID: "order-1", SegmentCount: 3}
		if err := d.sendHTTPCallback(context.Background(), rec, provider.DLR{}, dlrAggregate{Final: true}); err != nil {
			t.Fatal(err)
		}
		if rec.CallbackURL != tc.url {
			t.Fatal("stored customer URL was changed")
		}
	}
	if forwarded.Load() != 1 || direct.Load() != 6 {
		t.Fatalf("scope mismatch forwarded=%d direct=%d", forwarded.Load(), direct.Load())
	}
	d.ReloadCallbacks(config.DispatcherConfig{})
	if err := d.sendHTTPCallback(context.Background(), store.Pending{TenantID: rule.TenantID, CallbackURL: rule.CallbackURL}, provider.DLR{}, dlrAggregate{}); err != nil || forwarded.Load() != 1 || direct.Load() != 7 {
		t.Fatalf("rule removal not effective: %v", err)
	}
}

func TestForwardedHTTPFailureRetryAndRedirect(t *testing.T) {
	var bodies []string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Redirect(w, r, "/must-not-follow", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	rule := config.CallbackForwarding{TenantID: "test-c", CallbackURL: "http://callbacks.example:2761/sms/dr", TargetURL: "http://10.20.30.40:2761/sms/dr"}
	d := &Dispatcher{logger: slog.Default(), httpClient: newCallbackClient()}
	d.ReloadCallbacks(config.DispatcherConfig{CallbackForwarding: []config.CallbackForwarding{rule}})
	table := d.callbackForwards.Load()
	defer table.closeIdle()
	transport := table.routes[callbackForwardKey{rule.TenantID, rule.CallbackURL}].client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); !errors.Is(err, errUnsafeCallback) {
		t.Fatalf("forward dialer allowed another destination: %v", err)
	}
	// Only this trusted test fixture substitutes a local TCP destination.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "10.20.30.40:2761" {
			return nil, errUnsafeCallback
		}
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(srv.URL, "http://"))
	}
	rec := store.Pending{TenantID: rule.TenantID, CallbackURL: rule.CallbackURL, GatewayID: "gateway", SegmentCount: 1}
	event := provider.DLR{Provider: "p", ProviderID: "up", State: "DELIVRD", DoneAt: time.Now().UTC()}
	if err := d.sendHTTPCallback(context.Background(), rec, event, dlrAggregate{Final: true}); err == nil || errors.Is(err, errUnsafeCallback) {
		t.Fatalf("503 must remain retryable: %v", err)
	}
	if err := d.sendHTTPCallback(context.Background(), rec, event, dlrAggregate{Final: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.sendHTTPCallback(context.Background(), rec, event, dlrAggregate{Final: true}); !errors.Is(err, errUnsafeCallback) {
		t.Fatalf("redirect accepted: %v", err)
	}
	if calls.Load() != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("retry body changed or redirect followed: calls=%d", calls.Load())
	}
}

func TestCallbackForwardingConcurrentReload(t *testing.T) {
	d := &Dispatcher{logger: slog.Default()}
	rule := config.CallbackForwarding{TenantID: "test-c", CallbackURL: "http://callbacks.example/dr", TargetURL: "http://10.20.30.40:2761/dr"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				d.ReloadCallbacks(config.DispatcherConfig{CallbackForwarding: []config.CallbackForwarding{rule}})
				if table := d.callbackForwards.Load(); len(table.routes) != 1 {
					t.Error("partial rules became visible")
				}
			}
		}()
	}
	wg.Wait()
	d.callbackForwards.Load().closeIdle()
}

func TestPersistedReceiptUsesForwardingAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.json")
	st, err := store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rule := config.CallbackForwarding{TenantID: "test-c", CallbackURL: "http://callbacks.example:2761/sms/dr", TargetURL: "http://10.20.30.40:2761/sms/dr"}
	ctx := context.Background()
	if err := st.SaveMessage(ctx, testMessage("forward-existing")); err != nil {
		t.Fatal(err)
	}
	p := store.Pending{GatewayID: "forward-existing", TenantID: rule.TenantID, Provider: "test-provider", ProviderID: "test-id", SourceKind: "http", CallbackURL: rule.CallbackURL, ReliabilityManaged: true, SegmentIndex: 1, SegmentCount: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.SavePending(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.PrepareReceipt(ctx, store.ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: "DELIVRD", DoneAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	st, err = store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	bodies := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	d := &Dispatcher{store: st, logger: slog.Default(), httpClient: newCallbackClient()}
	d.ReloadCallbacks(config.DispatcherConfig{CallbackForwarding: []config.CallbackForwarding{rule}})
	table := d.callbackForwards.Load()
	defer table.closeIdle()
	transport := table.routes[callbackForwardKey{rule.TenantID, rule.CallbackURL}].client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(srv.URL, "http://"))
	}
	workerCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); d.receiptWorker(workerCtx, "delivery") }()
	defer func() { cancel(); <-finished }()
	waitReceiptState(t, st, "delivery:done", 1)
	if calls.Load() != 2 || <-bodies != <-bodies {
		t.Fatal("durable retry changed payload or attempt count")
	}
	stored, ok, err := st.GetPending(ctx, p.Provider, p.ProviderID)
	if err != nil || !ok || !stored.DLRDelivered || stored.CallbackURL != rule.CallbackURL {
		t.Fatalf("stored receipt changed URL or missed delivery: %+v %v", stored, err)
	}
}
