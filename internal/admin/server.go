package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/authutil"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

type Gateway interface {
	Config() config.Config
	UpdateConfig(config.Config) error
}

type smppStatusGateway interface {
	SMPPUpstreamStatuses() []smppclient.PoolStatus
}

type testSubmitGateway interface {
	SubmitTestMessage(context.Context, string, string, string) (dispatch.Receipt, error)
}

type ctxKey string

const sessionContextKey ctxKey = "admin-session"

type Server struct {
	gateway    Gateway
	configPath string
	sessions   *SessionStore
	limiter    *loginLimiter
	logger     *slog.Logger
	mux        *http.ServeMux
}

func New(gateway Gateway, configPath string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		gateway:    gateway,
		configPath: configPath,
		sessions:   NewSessionStore(8 * time.Hour),
		limiter:    newLoginLimiter(5, 15*time.Minute),
		logger:     logger,
		mux:        http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Close() {
	s.sessions.Close()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/admin/static/style.css", s.style)
	s.mux.HandleFunc("/admin/login", s.login)
	s.mux.HandleFunc("/admin/logout", s.requireSession(s.requireCSRF(s.logout)))
	s.mux.HandleFunc("/admin/", s.requireSession(s.admin))
}

func (s *Server) style(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(styleCSS))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.renderLogin(w, r, "")
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			s.renderLogin(w, r, "表单无效")
			return
		}
		ip := remoteIP(r, s.gateway.Config().TrustedProxies)
		if !s.limiter.Allow(ip) {
			s.renderLogin(w, r, "登录失败次数过多，请稍后再试")
			return
		}
		admin := s.gateway.Config().Admin
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if !authutil.ConstantTimeEqual(username, admin.Username) || !authutil.ConstantTimeEqual(password, admin.Password) {
			s.limiter.Fail(ip)
			s.renderLogin(w, r, "用户名或密码错误")
			return
		}
		session, err := s.sessions.New(username)
		if err != nil {
			http.Error(w, "create session failed", http.StatusInternalServerError)
			return
		}
		s.limiter.Reset(ip)
		setSessionCookie(w, session, r.TLS != nil)
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session := sessionFromContext(r.Context())
	s.sessions.Delete(session.Token)
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	if path == "" {
		path = "/"
	}
	switch {
	case r.Method == http.MethodGet && path == "/":
		s.dashboard(w, r)
	case r.Method == http.MethodGet && path == "/raw":
		s.rawForm(w, r, "")
	case r.Method == http.MethodPost && path == "/raw":
		s.requireCSRF(s.rawSave)(w, r)
	case r.Method == http.MethodGet && path == "/routes":
		s.routesList(w, r)
	case r.Method == http.MethodGet && path == "/routes/new":
		s.routeForm(w, r, "", "")
	case r.Method == http.MethodPost && path == "/routes":
		s.requireCSRF(s.routeCreate)(w, r)
	case r.Method == http.MethodGet && path == "/connections":
		s.connections(w, r, "")
	case r.Method == http.MethodPost && path == "/connections/testsend":
		s.requireCSRF(s.connectionTestSend)(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/routes/"):
		name := strings.TrimPrefix(path, "/routes/")
		if name == "" || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		s.routeForm(w, r, name, "")
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/routes/"):
		rest := strings.TrimPrefix(path, "/routes/")
		if strings.HasSuffix(rest, "/delete") {
			s.requireCSRF(s.routeDelete)(w, r)
			return
		}
		if rest == "" || strings.Contains(rest, "/") {
			http.NotFound(w, r)
			return
		}
		s.requireCSRF(s.routeUpdate)(w, r)
	case r.Method == http.MethodGet && isSectionPage(path):
		s.sectionForm(w, r, strings.TrimPrefix(path, "/"), "")
	case r.Method == http.MethodPost && isSectionPage(path):
		s.requireCSRF(s.sectionSave)(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		session, ok := s.sessions.Get(cookie.Value)
		if !ok {
			clearSessionCookie(w)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		session := sessionFromContext(r.Context())
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if session.CSRFToken == "" || !authutil.ConstantTimeEqual(r.FormValue("_csrf"), session.CSRFToken) {
			http.Error(w, "csrf token invalid", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func sessionFromContext(ctx context.Context) Session {
	session, _ := ctx.Value(sessionContextKey).(Session)
	return session
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]any{
		"Title": "登录",
		"Error": errMsg,
	}
	if err := loginTemplate.ExecuteTemplate(w, "login", data); err != nil {
		s.logger.Error("render login failed", "err", err)
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	t, ok := pageTemplates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	session := sessionFromContext(r.Context())
	data["CSRFToken"] = session.CSRFToken
	data["Username"] = session.Username
	data["Flash"] = readFlash(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		s.logger.Error("render template failed", "err", err, "template", name)
	}
}

func readFlash(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("mysmpp_admin_flash")
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "mysmpp_admin_flash",
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	msg, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return cookie.Value
	}
	return msg
}

func flashAndRedirect(w http.ResponseWriter, r *http.Request, msg, to string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "mysmpp_admin_flash",
		Value:    url.QueryEscape(msg),
		Path:     "/admin",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cfg := s.gateway.Config()
	statuses := s.smppStatuses()
	totalConnections, boundConnections := smppStatusSummary(statuses)
	s.render(w, r, "dashboard.html", map[string]any{
		"Title":            "概览",
		"Active":           "dashboard",
		"RouteCount":       len(cfg.Routes),
		"ProviderCount":    len(cfg.Providers),
		"ESMECount":        len(cfg.ESMEs),
		"InboundCount":     len(cfg.Inbound),
		"OutboundCount":    len(cfg.Outbound),
		"ClientCount":      len(cfg.Clients),
		"UpstreamProvider": len(statuses),
		"UpstreamBound":    boundConnections,
		"UpstreamTotal":    totalConnections,
		"ConfigPath":       s.configPath,
	})
}

func (s *Server) connections(w http.ResponseWriter, r *http.Request, errMsg string) {
	statuses := s.smppStatuses()
	totalConnections, boundConnections := smppStatusSummary(statuses)
	_, canSubmit := s.gateway.(testSubmitGateway)
	s.render(w, r, "connections.html", map[string]any{
		"Title":            "SMPP upstream connections",
		"Active":           "connections",
		"Statuses":         statuses,
		"TotalConnections": totalConnections,
		"BoundConnections": boundConnections,
		"CanSubmit":        canSubmit,
		"Error":            errMsg,
	})
}

func (s *Server) connectionTestSend(w http.ResponseWriter, r *http.Request) {
	submitter, ok := s.gateway.(testSubmitGateway)
	if !ok {
		s.connections(w, r, "test send is not available without dispatcher")
		return
	}
	from := strings.TrimSpace(r.FormValue("from"))
	to := strings.TrimSpace(r.FormValue("to"))
	text := strings.TrimSpace(r.FormValue("text"))
	if from == "" || to == "" || text == "" {
		s.connections(w, r, "from, to, and text are required")
		return
	}
	receipt, err := submitter.SubmitTestMessage(r.Context(), from, to, text)
	if err != nil {
		s.connections(w, r, err.Error())
		return
	}
	flashAndRedirect(w, r, "test message submitted: "+receipt.GatewayID, "/admin/connections")
}

func (s *Server) rawForm(w http.ResponseWriter, r *http.Request, errMsg string) {
	data, err := json.MarshalIndent(s.gateway.Config(), "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "raw.html", map[string]any{
		"Title":  "原始 JSON",
		"Active": "raw",
		"Config": string(data),
		"Error":  errMsg,
	})
}

func (s *Server) rawSave(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := json.Unmarshal([]byte(r.FormValue("config")), &cfg); err != nil {
		s.rawForm(w, r, "JSON 无效: "+err.Error())
		return
	}
	if err := s.applyConfig(cfg); err != nil {
		s.rawForm(w, r, err.Error())
		return
	}
	flashAndRedirect(w, r, "配置已保存", "/admin/raw")
}

func (s *Server) routesList(w http.ResponseWriter, r *http.Request) {
	cfg := s.gateway.Config()
	s.render(w, r, "routes_list.html", map[string]any{
		"Title":  "线路配置",
		"Active": "routes",
		"Routes": cfg.Routes,
	})
}

func (s *Server) routeForm(w http.ResponseWriter, r *http.Request, name, errMsg string) {
	cfg := s.gateway.Config()
	route := config.RouteConfig{}
	isEdit := false
	if name != "" {
		found, ok := findRoute(cfg.Routes, name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		route = found
		isEdit = true
	}
	s.render(w, r, "route_form.html", map[string]any{
		"Title":     ternary(isEdit, "编辑线路", "新建线路"),
		"Active":    "routes",
		"Route":     route,
		"IsEdit":    isEdit,
		"Providers": cfg.Providers,
		"Prefix":    strings.Join(route.Prefix, "\n"),
		"Error":     errMsg,
	})
}

func (s *Server) routeCreate(w http.ResponseWriter, r *http.Request) {
	route := routeFromForm(r)
	cfg := cloneConfig(s.gateway.Config())
	if _, ok := findRoute(cfg.Routes, route.Name); ok {
		s.routeFormWithValue(w, r, route, false, "线路名已存在")
		return
	}
	cfg.Routes = append(cfg.Routes, route)
	if err := s.applyConfig(cfg); err != nil {
		s.routeFormWithValue(w, r, route, false, err.Error())
		return
	}
	flashAndRedirect(w, r, "线路已创建", "/admin/routes")
}

func (s *Server) routeUpdate(w http.ResponseWriter, r *http.Request) {
	oldName := strings.TrimPrefix(r.URL.Path, "/admin/routes/")
	formRoute := routeFromForm(r)
	cfg := cloneConfig(s.gateway.Config())
	route, ok := findRoute(cfg.Routes, oldName)
	if !ok {
		http.NotFound(w, r)
		return
	}
	route.Provider = formRoute.Provider
	route.Priority = formRoute.Priority
	route.Prefix = formRoute.Prefix
	for i := range cfg.Routes {
		if cfg.Routes[i].Name == oldName {
			cfg.Routes[i] = route
			break
		}
	}
	if err := s.applyConfig(cfg); err != nil {
		s.routeFormWithValue(w, r, route, true, err.Error())
		return
	}
	flashAndRedirect(w, r, "线路已保存", "/admin/routes")
}

func (s *Server) routeDelete(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/routes/")
	name := strings.TrimSuffix(rest, "/delete")
	cfg := cloneConfig(s.gateway.Config())
	if _, ok := findRoute(cfg.Routes, name); !ok {
		http.NotFound(w, r)
		return
	}
	cfg.Routes = removeRoute(cfg.Routes, name)
	if err := s.applyConfig(cfg); err != nil {
		flashAndRedirect(w, r, err.Error(), "/admin/routes")
		return
	}
	flashAndRedirect(w, r, "线路已删除", "/admin/routes")
}

func (s *Server) routeFormWithValue(w http.ResponseWriter, r *http.Request, route config.RouteConfig, isEdit bool, errMsg string) {
	cfg := s.gateway.Config()
	s.render(w, r, "route_form.html", map[string]any{
		"Title":     ternary(isEdit, "编辑线路", "新建线路"),
		"Active":    "routes",
		"Route":     route,
		"IsEdit":    isEdit,
		"Providers": cfg.Providers,
		"Prefix":    strings.Join(route.Prefix, "\n"),
		"Error":     errMsg,
	})
}

func (s *Server) sectionForm(w http.ResponseWriter, r *http.Request, section, errMsg string) {
	body, err := sectionJSON(s.gateway.Config(), section)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "section_json.html", map[string]any{
		"Title":        sectionTitle(section),
		"Active":       section,
		"Section":      section,
		"SectionTitle": sectionTitle(section),
		"Config":       body,
		"Error":        errMsg,
		"SMPPStatuses": s.smppStatuses(),
	})
}

func (s *Server) smppStatuses() []smppclient.PoolStatus {
	gateway, ok := s.gateway.(smppStatusGateway)
	if !ok {
		return nil
	}
	return gateway.SMPPUpstreamStatuses()
}

func smppStatusSummary(statuses []smppclient.PoolStatus) (int, int) {
	total := 0
	bound := 0
	for _, status := range statuses {
		for _, conn := range status.Connections {
			total++
			if conn.Bound {
				bound++
			}
		}
	}
	return total, bound
}

func (s *Server) sectionSave(w http.ResponseWriter, r *http.Request) {
	section := strings.TrimPrefix(r.URL.Path, "/admin/")
	cfg := cloneConfig(s.gateway.Config())
	if err := applySectionJSON(&cfg, section, r.FormValue("config")); err != nil {
		s.sectionForm(w, r, section, err.Error())
		return
	}
	if err := s.applyConfig(cfg); err != nil {
		s.sectionForm(w, r, section, err.Error())
		return
	}
	flashAndRedirect(w, r, sectionTitle(section)+"已保存", "/admin/"+section)
}

func (s *Server) applyConfig(cfg config.Config) error {
	if err := s.gateway.UpdateConfig(cfg); err != nil {
		return err
	}
	if err := AtomicWrite(s.configPath, s.gateway.Config()); err != nil {
		return fmt.Errorf("运行时已更新，但写入配置文件失败: %w", err)
	}
	return nil
}

func routeFromForm(r *http.Request) config.RouteConfig {
	return config.RouteConfig{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Provider: strings.TrimSpace(r.FormValue("provider")),
		Priority: atoi(r.FormValue("priority"), 0),
		Prefix:   parseLines(r.FormValue("prefix")),
	}
}

func parseLines(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		item := strings.TrimSpace(line)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func findRoute(routes []config.RouteConfig, name string) (config.RouteConfig, bool) {
	for _, route := range routes {
		if route.Name == name {
			return route, true
		}
	}
	return config.RouteConfig{}, false
}

func removeRoute(routes []config.RouteConfig, name string) []config.RouteConfig {
	out := make([]config.RouteConfig, 0, len(routes))
	for _, route := range routes {
		if route.Name != name {
			out = append(out, route)
		}
	}
	return out
}

func cloneConfig(cfg config.Config) config.Config {
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var out config.Config
	if err := json.Unmarshal(data, &out); err != nil {
		return cfg
	}
	return out
}

func atoi(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return n
}

func ternary(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

func isSectionPage(path string) bool {
	switch path {
	case "/providers", "/esmes", "/clients", "/inbound", "/outbound", "/risk", "/smpp":
		return true
	default:
		return false
	}
}

func sectionTitle(name string) string {
	switch name {
	case "providers":
		return "上游供应商"
	case "esmes":
		return "下游 ESME"
	case "clients":
		return "HTTP 客户端"
	case "inbound":
		return "入站规则"
	case "outbound":
		return "出站规则"
	case "risk":
		return "风控"
	case "smpp":
		return "SMPP"
	default:
		return name
	}
}

func sectionJSON(cfg config.Config, section string) (string, error) {
	var value any
	switch section {
	case "providers":
		value = cfg.Providers
	case "esmes":
		value = cfg.ESMEs
	case "clients":
		value = cfg.Clients
	case "inbound":
		value = cfg.Inbound
	case "outbound":
		value = cfg.Outbound
	case "risk":
		value = cfg.Risk
	case "smpp":
		value = cfg.SMPP
	default:
		return "", fmt.Errorf("unknown section %q", section)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func applySectionJSON(cfg *config.Config, section, body string) error {
	switch section {
	case "providers":
		var value []config.ProviderConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.Providers = value
	case "esmes":
		var value []config.ESMECred
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.ESMEs = value
	case "clients":
		var value []config.ClientAuth
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.Clients = value
	case "inbound":
		var value []config.HTTPRuleConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.Inbound = value
	case "outbound":
		var value []config.HTTPRuleConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.Outbound = value
	case "risk":
		var value config.RiskConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.Risk = value
	case "smpp":
		var value config.SMPPConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return fmt.Errorf("JSON 无效: %w", err)
		}
		cfg.SMPP = value
	default:
		return fmt.Errorf("unknown section %q", section)
	}
	return nil
}
