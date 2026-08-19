package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

type fakeGateway struct {
	mu  sync.Mutex
	cfg config.Config
}

type fakeStatusGateway struct {
	*fakeGateway
	statuses  []smppclient.PoolStatus
	submitted []dispatch.Envelope
}

func (g *fakeGateway) Config() config.Config {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg
}

func (g *fakeGateway) UpdateConfig(cfg config.Config) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	g.cfg = cfg
	g.mu.Unlock()
	return nil
}

func (g *fakeStatusGateway) SMPPUpstreamStatuses() []smppclient.PoolStatus {
	return g.statuses
}

func (g *fakeStatusGateway) SubmitTestMessage(ctx context.Context, from, to, text string) (dispatch.Receipt, error) {
	g.submitted = append(g.submitted, dispatch.Envelope{
		From: from,
		To:   to,
		Text: text,
	})
	return dispatch.Receipt{
		GatewayID:  "admin-test-1",
		ProviderID: "upstream-1",
		Provider:   "smpp-up",
		Route:      "default",
		State:      "submitted",
	}, nil
}

func TestAdminLoginAndDashboard(t *testing.T) {
	srv := New(newFakeGateway(), "", nil)
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("expected redirect to login, got %d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	cookie := loginCookie(t, srv)
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "概览") {
		t.Fatalf("dashboard did not render expected content: %s", rec.Body.String())
	}
}

func TestAdminRoutesRequireCSRF(t *testing.T) {
	srv := New(newFakeGateway(), "", nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)

	form := url.Values{
		"name":     {"mobile"},
		"provider": {"mock-a"},
		"priority": {"10"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/routes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without csrf, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminRouteCreatePersistsConfig(t *testing.T) {
	cfgPath := t.TempDir() + "/config.json"
	srv := New(newFakeGateway(), cfgPath, nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)
	csrf := csrfFromPage(t, srv, cookie, "/admin/routes/new")

	form := url.Values{
		"_csrf":    {csrf},
		"name":     {"mobile"},
		"prefix":   {"138\n139"},
		"provider": {"mock-a"},
		"priority": {"10"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/routes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/routes" {
		t.Fatalf("expected redirect to routes, got %d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	got := srv.gateway.Config()
	if len(got.Routes) != 1 || got.Routes[0].Name != "mobile" || got.Routes[0].Priority != 10 {
		t.Fatalf("route not applied to runtime config: %+v", got.Routes)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Routes) != 1 || persisted.Routes[0].Name != "mobile" || len(persisted.Routes[0].Prefix) != 2 {
		t.Fatalf("route not persisted: %+v", persisted.Routes)
	}
}

func TestAdminRouteUpdatePreservesAdvancedAddressRules(t *testing.T) {
	gateway := newFakeGateway()
	gateway.cfg.Routes = []config.RouteConfig{{
		Name:     "mobile",
		Prefix:   []string{"86"},
		Provider: "mock-a",
		Priority: 1,
		AddrRewrite: config.AddrRewriteConfig{
			StripTrunkZeroAfterCC: true,
			CountryCode:           "86",
		},
		DestAddr: config.DestAddrConfig{CountryLengthMode: "strict"},
	}}
	srv := New(gateway, t.TempDir()+"/config.json", nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)
	csrf := csrfFromPage(t, srv, cookie, "/admin/routes/mobile")
	form := url.Values{
		"_csrf":    {csrf},
		"provider": {"mock-a"},
		"priority": {"20"},
		"prefix":   {"86\n852"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/routes/mobile", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	got := srv.gateway.Config().Routes[0]
	if got.Priority != 20 || len(got.Prefix) != 2 {
		t.Fatalf("form-managed fields were not updated: %+v", got)
	}
	if !got.AddrRewrite.StripTrunkZeroAfterCC || got.AddrRewrite.CountryCode != "86" || got.DestAddr.CountryLengthMode != "strict" {
		t.Fatalf("advanced address rules were lost: %+v", got)
	}
}

func TestAdminSectionSavePersistsProviders(t *testing.T) {
	cfgPath := t.TempDir() + "/config.json"
	srv := New(newFakeGateway(), cfgPath, nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)
	csrf := csrfFromPage(t, srv, cookie, "/admin/providers")

	providers := `[{"name":"mock-a","enabled":true},{"name":"mock-b","enabled":true}]`
	form := url.Values{
		"_csrf":  {csrf},
		"config": {providers},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/providers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/providers" {
		t.Fatalf("expected redirect to providers, got %d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	got := srv.gateway.Config()
	if len(got.Providers) != 2 || got.Providers[1].Name != "mock-b" {
		t.Fatalf("providers not applied to runtime config: %+v", got.Providers)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Providers) != 2 || persisted.Providers[1].Name != "mock-b" {
		t.Fatalf("providers not persisted: %+v", persisted.Providers)
	}
}

func TestAdminTenantSectionRoundTripsLimits(t *testing.T) {
	cfgPath := t.TempDir() + "/config.json"
	srv := New(newFakeGateway(), cfgPath, nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)
	csrf := csrfFromPage(t, srv, cookie, "/admin/tenants")
	form := url.Values{
		"_csrf":  {csrf},
		"config": {`[{"tenant_id":"customer-a","limits":{"tps":25,"burst":50,"daily_segments":10000,"timezone":"Asia/Shanghai"}}]`},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/tenants" {
		t.Fatalf("tenant save failed: status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	got := srv.gateway.Config().Tenants
	if len(got) != 1 || got[0].TenantID != "customer-a" || got[0].Limits.DailySegments != 10000 {
		t.Fatalf("tenant limits not applied: %+v", got)
	}
}

func TestAdminConnectionsRenderSMPPStatus(t *testing.T) {
	gateway := &fakeStatusGateway{
		fakeGateway: newFakeGateway(),
		statuses: []smppclient.PoolStatus{{
			Name:     "smpp-up",
			Endpoint: "127.0.0.1:2775",
			Connections: []smppclient.ConnectionStatus{{
				ID:             1,
				State:          "bound",
				Bound:          true,
				InFlight:       2,
				WindowSize:     16,
				SubmitOK:       7,
				SubmitFailed:   1,
				DeliverSMCount: 3,
			}},
		}},
	}
	srv := New(gateway, "", nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected connections 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"SMPP upstream connections", "smpp-up", "127.0.0.1:2775", "7 ok", "3"} {
		if !strings.Contains(body, want) {
			t.Fatalf("connections page missing %q: %s", want, body)
		}
	}
}

func TestAdminConnectionTestSend(t *testing.T) {
	gateway := &fakeStatusGateway{fakeGateway: newFakeGateway()}
	srv := New(gateway, "", nil)
	defer srv.Close()
	cookie := loginCookie(t, srv)
	csrf := csrfFromPage(t, srv, cookie, "/admin/connections")

	form := url.Values{
		"_csrf": {csrf},
		"from":  {"mysmpp"},
		"to":    {"13800138000"},
		"text":  {"admin smoke"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/connections/testsend", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/connections" {
		t.Fatalf("expected redirect to connections, got %d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(gateway.submitted) != 1 || gateway.submitted[0].To != "13800138000" || gateway.submitted[0].Text != "admin smoke" {
		t.Fatalf("test submit not called correctly: %+v", gateway.submitted)
	}
}

func loginCookie(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	form := url.Values{
		"username": {"admin"},
		"password": {"secret"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected login redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatal("session cookie not set")
	return nil
}

func csrfFromPage(t *testing.T, srv *Server, cookie *http.Cookie, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected csrf page 200, got %d: %s", rec.Code, rec.Body.String())
	}
	re := regexp.MustCompile(`name="_csrf" value="([^"]+)"`)
	m := re.FindStringSubmatch(rec.Body.String())
	if len(m) != 2 {
		t.Fatalf("csrf token not found in %s", rec.Body.String())
	}
	return m[1]
}

func newFakeGateway() *fakeGateway {
	cfg := config.Default()
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "secret"}
	cfg.SMPP.Password = "secret"
	cfg.Providers = []config.ProviderConfig{{
		Name:    "mock-a",
		Enabled: true,
	}}
	cfg.Routes = nil
	return &fakeGateway{cfg: cfg}
}
