package httpgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func assertNoRoutingFields(t *testing.T, body []byte) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	for key := range object {
		switch strings.ToLower(key) {
		case "provider", "provider_id", "providerid", "route":
			t.Fatalf("tenant response contains internal field %q: %s", key, body)
		}
	}
}

func TestMessageQueryHidesOnlyRoutingAndPreservesAdminView(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	msg := message.New("gateway-public", message.DirectionMT, "Brand", "8613800138000", "hello\x00中文")
	msg.TenantID, msg.AccountID, msg.ClientMsgID = "customer", "client-a", "order-public"
	msg.Provider, msg.ProviderID, msg.Route = "private-supplier", "private-upstream-id", "private-route"
	msg.SourceKind, msg.SourceID = "http", ""
	msg.State, msg.ErrorCode = "UNDELIV", 42
	msg.Segments = message.Split(msg.Text, message.SplitOptions{})
	msg.Metadata = map[string]string{"client_id": "client-a", "biz_type": "verification"}
	msg.SentAt = msg.SubmittedAt.Add(time.Second)
	msg.DoneAt = msg.SentAt.Add(time.Second)
	if err := st.SaveMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	other := message.New("other-account", message.DirectionMT, "Brand", "8613800138000", "private message")
	other.Metadata["client_id"] = "client-b"
	if err := st.SaveMessage(ctx, other); err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(original, &full); err != nil {
		t.Fatal(err)
	}
	for _, admin := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenant", true: "admin"}[admin], func(t *testing.T) {
			cfg := config.Default()
			cfg.Admin = config.AdminConfig{Username: "admin", Password: "test-secret"}
			req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
			if admin {
				req.SetBasicAuth("admin", "test-secret")
				// An untrusted header must not change the authenticated admin view.
				req.Header.Set("X-Client-ID", "client-a")
			} else {
				enableClientAuth(&cfg)
				addClientAuth(req)
			}
			rec := httptest.NewRecorder()
			New(cfg, st).Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("query status=%d body=%s", rec.Code, rec.Body.String())
			}
			var got []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if admin {
				wantCount = 2
			}
			if len(got) != wantCount {
				t.Fatalf("account isolation changed: %s", rec.Body.String())
			}
			var actual map[string]any
			for _, item := range got {
				if item["ID"] == msg.ID {
					actual = item
				}
			}
			want := make(map[string]any, len(full))
			for key, value := range full {
				want[key] = value
			}
			if !admin {
				for _, key := range []string{"Provider", "ProviderID", "Route"} {
					delete(want, key)
				}
				if strings.Contains(rec.Body.String(), "private-") {
					t.Fatalf("query leaked routing: %s", rec.Body.String())
				}
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("message fields changed: got=%+v want=%+v", actual, want)
			}
		})
	}
	stored, ok, err := st.GetMessage(ctx, msg.ID)
	if err != nil || !ok || !reflect.DeepEqual(stored, msg) {
		t.Fatalf("response projection mutated stored message: %+v %v", stored, err)
	}
}

func TestMessageErrorsKeepStatusWithoutLeakingInternalDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		text   string
	}{
		{"rate", dispatch.ErrRateExceeded, 429, dispatch.ErrRateExceeded.Error()},
		{"quota", dispatch.ErrQuotaExceeded, 429, dispatch.ErrQuotaExceeded.Error()},
		{"disabled", dispatch.ErrTenantDisabled, 403, dispatch.ErrTenantDisabled.Error()},
		{"blocked", dispatch.ErrBlocked, 403, dispatch.ErrBlocked.Error()},
		{"destination", dispatch.ErrInvalidDestAddr, 400, dispatch.ErrInvalidDestAddr.Error()},
		{"length", dispatch.ErrInvalidMessageLength, 400, dispatch.ErrInvalidMessageLength.Error()},
		{"route", dispatch.ErrNoRoute, 502, dispatch.ErrNoRoute.Error()},
		{"storage", errors.New("storage unavailable"), 502, "message submission failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			internalErr := fmt.Errorf("%w: private-route private-supplier private-upstream-id", tc.err)
			for _, admin := range []bool{false, true} {
				cfg := config.Default()
				cfg.Admin = config.AdminConfig{Username: "admin", Password: "test-secret"}
				if !admin {
					enableClientAuth(&cfg)
				}
				st := failingStore{Store: store.NewMemory(), err: internalErr}
				d := dispatch.New(nil, provider.NewRegistry(), nil, testDispatcherConfig(), st)
				defer d.Close()
				d.ReloadRoutes([]config.RouteConfig{{Name: "route", Provider: "p"}}, []config.ProviderConfig{{Name: "p", Enabled: true}})
				g := NewWithDispatcher(cfg, st, d)
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"Brand","to":"+8613800138000","text":"test"}`))
				if admin {
					req.SetBasicAuth("admin", "test-secret")
				} else {
					addClientAuth(req)
				}
				rec := httptest.NewRecorder()
				g.Handler().ServeHTTP(rec, req)
				wantText := tc.text
				if admin {
					wantText = internalErr.Error()
				}
				if rec.Code != tc.status || rec.Body.String() != wantText+"\n" {
					t.Fatalf("admin=%v status=%d body=%q want=%q", admin, rec.Code, rec.Body.String(), wantText)
				}
			}
		})
	}
}

func TestMessageQueryAndFallbackErrorsArePrivate(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			cfg := config.Default()
			enableClientAuth(&cfg)
			st := failingStore{Store: store.NewMemory(), err: errors.New("database private-route private-supplier")}
			req := httptest.NewRequest(method, "/v1/messages", strings.NewReader(`{"from":"Brand","to":"+8613800138000","text":"test"}`))
			addClientAuth(req)
			rec := httptest.NewRecorder()
			New(cfg, st).Handler().ServeHTTP(rec, req)
			want := "message query failed\n"
			if method == http.MethodPost {
				want = "message storage failed\n"
			}
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != want {
				t.Fatalf("unexpected public error: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUnavailableProviderDoesNotExposeMatchedRoute(t *testing.T) {
	cfg := config.Default()
	enableClientAuth(&cfg)
	cfg.Routes = []config.RouteConfig{{Name: "private-route", Provider: "private-supplier"}}
	cfg.Providers = []config.ProviderConfig{{Name: "private-supplier", Enabled: false}}
	st := store.NewMemory()
	d := dispatch.New(nil, provider.NewRegistry(), nil, testDispatcherConfig(), st)
	defer d.Close()
	d.ReloadRoutes(cfg.Routes, cfg.Providers)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"from":"Brand","to":"+8613800138000","text":"test"}`))
	addClientAuth(req)
	rec := httptest.NewRecorder()
	NewWithDispatcher(cfg, st, d).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Body.String() != dispatch.ErrNoRoute.Error()+"\n" {
		t.Fatalf("unexpected no-provider response: %d %s", rec.Code, rec.Body.String())
	}
}
