package httpgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/splendideXmendax/mysmpp/internal/authutil"
	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/dispatch"
	"github.com/splendideXmendax/mysmpp/internal/httprule"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type Gateway struct {
	mu         sync.RWMutex
	cfg        config.Config
	store      store.Store
	dispatcher *dispatch.Dispatcher
	registry   *provider.Registry
	ctx        context.Context
	mux        *http.ServeMux
	riskMu     sync.Mutex
	riskCounts map[string]rateWindow
	riskSweep  time.Time
}

type rateWindow struct {
	start     time.Time
	expiresAt time.Time
	count     int
}

func New(cfg config.Config, st store.Store) *Gateway {
	g := &Gateway{cfg: cfg, store: st, mux: http.NewServeMux(), riskCounts: map[string]rateWindow{}}
	g.routes()
	return g
}

func NewWithDispatcher(cfg config.Config, st store.Store, dispatcher *dispatch.Dispatcher, extras ...any) *Gateway {
	g := &Gateway{cfg: cfg, store: st, dispatcher: dispatcher, ctx: context.Background(), mux: http.NewServeMux(), riskCounts: map[string]rateWindow{}}
	for _, extra := range extras {
		switch v := extra.(type) {
		case *provider.Registry:
			g.registry = v
		case context.Context:
			if v != nil {
				g.ctx = v
			}
		}
	}
	g.routes()
	return g
}

func (g *Gateway) Handler() http.Handler {
	return g.mux
}

func (g *Gateway) Mount(pattern string, handler http.Handler) {
	g.mux.Handle(pattern, handler)
}

func (g *Gateway) routes() {
	g.mux.HandleFunc("/healthz", g.health)
	g.mux.HandleFunc("/v1/messages", g.messages)
	g.mux.HandleFunc("/v1/config", g.requireAdmin(g.configAPI))
	g.mux.HandleFunc("/ui/config", g.requireAdmin(g.configPage))
	g.mux.HandleFunc("/", g.dynamicInbound)
}

func (g *Gateway) health(w http.ResponseWriter, _ *http.Request) {
	pending, _ := g.store.PendingSize(context.Background())
	outbox, _ := g.store.OutboxDepth(context.Background(), "pending")
	status := "ok"
	if outbox > 10000 {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status,
		"checks": map[string]any{
			"storage":       "ok",
			"pending_size":  pending,
			"outbox_depth":  outbox,
			"smpp_listener": "ok",
		},
	})
}

func (g *Gateway) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin := g.Config().Admin
		if admin.Username == "" || admin.Password == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="mysmpp-admin"`)
			http.Error(w, "admin credentials are required", http.StatusUnauthorized)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || !authutil.ConstantTimeEqual(username, admin.Username) || !authutil.ConstantTimeEqual(password, admin.Password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mysmpp-admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (g *Gateway) messages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		messages, err := g.store.ListMessagesPage(r.Context(), store.ListOptions{
			Limit:  intQuery(r, "limit", 100),
			Offset: intQuery(r, "offset", 0),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, messages)
	case http.MethodPost:
		var req struct {
			From         string            `json:"from"`
			To           string            `json:"to"`
			Text         string            `json:"text"`
			ClientMsgID  string            `json:"client_msg_id"`
			CallbackURL  string            `json:"callback_url"`
			CallbackRule string            `json:"callback_rule"`
			Meta         map[string]string `json:"meta"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		clientID, ok := g.authorizeClient(w, r)
		if !ok {
			return
		}
		if err := validateSubmitRequest(req.From, req.To, req.Text, req.ClientMsgID, req.CallbackURL, req.Meta); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if blocked, reason := g.applyRisk(r.Context(), clientID, req.From, req.To, req.Text, req.Meta); blocked {
			http.Error(w, reason, http.StatusTooManyRequests)
			return
		}
		if g.dispatcher != nil {
			receipt, err := g.dispatcher.Submit(r.Context(), dispatch.Envelope{
				From:               req.From,
				To:                 req.To,
				Text:               req.Text,
				ClientID:           clientID,
				ClientMsgID:        req.ClientMsgID,
				Encoding:           message.DetectEncoding(req.Text),
				RegisteredDelivery: 1,
				Source: dispatch.SubmitSource{
					Kind:         dispatch.SourceHTTPAPI,
					CallbackURL:  req.CallbackURL,
					CallbackRule: req.CallbackRule,
				},
				ReceivedAt: time.Now().UTC(),
				Meta:       req.Meta,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			writeJSON(w, http.StatusAccepted, receipt)
			return
		}
		cfg := g.Config()
		msg := message.New(newID(), message.DirectionMT, req.From, req.To, req.Text)
		msg.Metadata = req.Meta
		msg.Segments = message.Split(req.Text, message.SplitOptions{ForceEncoding: msg.Encoding})
		if route, ok := router.New(cfg.Routes).Match(msg); ok {
			msg.Route = route.Name
			msg.Provider = route.Provider
		}
		if err := g.store.SaveMessage(r.Context(), msg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, msg)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) authorizeClient(w http.ResponseWriter, r *http.Request) (string, bool) {
	cfg := g.Config()
	if len(cfg.Clients) == 0 {
		return r.Header.Get("X-Client-ID"), true
	}
	clientID := r.Header.Get("X-Client-ID")
	token := r.Header.Get("X-Token")
	if clientID == "" || token == "" {
		http.Error(w, "client credentials are required", http.StatusUnauthorized)
		return "", false
	}
	for _, client := range cfg.Clients {
		if !client.Enabled || !authutil.ConstantTimeEqual(client.ClientID, clientID) {
			continue
		}
		if !authutil.ConstantTimeEqual(client.Token, token) {
			break
		}
		if len(client.AllowedIPs) > 0 && !requestIPAllowed(r, client.AllowedIPs) {
			http.Error(w, "client ip is not allowed", http.StatusForbidden)
			return "", false
		}
		return clientID, true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", false
}

func requestIPAllowed(r *http.Request, allowed []string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, item := range allowed {
		if prefix, err := netip.ParsePrefix(item); err == nil {
			if prefix.Contains(ip) {
				return true
			}
			continue
		}
		if allowedIP, err := netip.ParseAddr(item); err == nil && allowedIP == ip {
			return true
		}
	}
	return false
}

func validateSubmitRequest(from, to, text, clientMsgID, callbackURL string, meta map[string]string) error {
	if utf8.RuneCountInString(from) < 1 || utf8.RuneCountInString(from) > 32 {
		return fmt.Errorf("from must be 1-32 characters")
	}
	if !validPhone(to) {
		return fmt.Errorf("to must be E.164 or 11 digits")
	}
	if utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 1000 {
		return fmt.Errorf("text must be 1-1000 characters")
	}
	if clientMsgID != "" && (utf8.RuneCountInString(clientMsgID) > 64 || strings.ContainsAny(clientMsgID, " \t\r\n")) {
		return fmt.Errorf("client_msg_id must be 1-64 non-space characters")
	}
	if callbackURL != "" {
		u, err := url.Parse(callbackURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("callback_url must be https")
		}
	}
	if len(meta) > 10 {
		return fmt.Errorf("meta can contain at most 10 keys")
	}
	for k, v := range meta {
		if k == "" || utf8.RuneCountInString(v) > 200 {
			return fmt.Errorf("meta values must be at most 200 characters")
		}
	}
	return nil
}

func validPhone(value string) bool {
	if len(value) == 11 && allDigits(value) {
		return true
	}
	if strings.HasPrefix(value, "+") && len(value) >= 8 && len(value) <= 16 && allDigits(value[1:]) {
		return true
	}
	return false
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func (g *Gateway) applyRisk(ctx context.Context, clientID, from, to, text string, meta map[string]string) (bool, string) {
	cfg := g.Config()
	for _, prefix := range cfg.Risk.BlockedToPrefix {
		if prefix != "" && strings.HasPrefix(to, prefix) {
			g.saveBlocked(ctx, from, to, text, "blocked_prefix", meta)
			return true, "blocked destination"
		}
	}
	lowerText := strings.ToLower(text)
	for _, keyword := range cfg.Risk.BlockedKeywords {
		if keyword != "" && strings.Contains(lowerText, strings.ToLower(keyword)) {
			g.saveBlocked(ctx, from, to, text, "blocked_keyword", meta)
			return true, "blocked keyword"
		}
	}
	if cfg.Risk.PerNumberPerMinute > 0 && !g.allowRate("num:min:"+to, time.Minute, cfg.Risk.PerNumberPerMinute) {
		g.saveBlocked(ctx, from, to, text, "number_rate_minute", meta)
		return true, "number rate limit exceeded"
	}
	if cfg.Risk.PerNumberPerDay > 0 && !g.allowRate("num:day:"+to, 24*time.Hour, cfg.Risk.PerNumberPerDay) {
		g.saveBlocked(ctx, from, to, text, "number_rate_day", meta)
		return true, "number daily limit exceeded"
	}
	if clientID != "" && cfg.Risk.PerClientPerSecond > 0 && !g.allowRate("client:sec:"+clientID, time.Second, cfg.Risk.PerClientPerSecond) {
		g.saveBlocked(ctx, from, to, text, "client_rate_second", meta)
		return true, "client rate limit exceeded"
	}
	return false, ""
}

func (g *Gateway) allowRate(key string, window time.Duration, limit int) bool {
	now := time.Now()
	g.riskMu.Lock()
	defer g.riskMu.Unlock()
	if g.riskSweep.IsZero() || now.Sub(g.riskSweep) >= time.Minute {
		for key, rec := range g.riskCounts {
			if !rec.expiresAt.IsZero() && now.After(rec.expiresAt) {
				delete(g.riskCounts, key)
			}
		}
		g.riskSweep = now
	}
	rec := g.riskCounts[key]
	if rec.start.IsZero() || now.Sub(rec.start) >= window {
		g.riskCounts[key] = rateWindow{start: now, expiresAt: now.Add(window), count: 1}
		return true
	}
	if rec.count >= limit {
		return false
	}
	rec.count++
	g.riskCounts[key] = rec
	return true
}

func (g *Gateway) saveBlocked(ctx context.Context, from, to, text, reason string, meta map[string]string) {
	msg := message.New(newID(), message.DirectionMT, from, to, text)
	msg.State = "blocked"
	msg.Metadata = map[string]string{"reason": reason}
	for k, v := range meta {
		msg.Metadata[k] = v
	}
	_ = g.store.SaveMessage(ctx, msg)
}

func (g *Gateway) Config() config.Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

func (g *Gateway) UpdateConfig(cfg config.Config) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	var providers map[string]provider.Provider
	if g.registry != nil {
		providers = provider.BuildProviders(g.ctx, cfg)
	}
	routes := cfg.Routes
	providerCfgs := cfg.Providers
	g.mu.Lock()
	g.cfg = cfg
	g.mu.Unlock()
	if g.registry != nil {
		g.registry.Replace(providers)
	}
	if g.dispatcher != nil {
		g.dispatcher.ReloadRoutes(routes, providerCfgs)
	}
	return nil
}

func (g *Gateway) configAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, g.Config())
	case http.MethodPut, http.MethodPost:
		var cfg config.Config
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := g.UpdateConfig(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, g.Config())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) configPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(configPageHTML))
}

func (g *Gateway) dynamicInbound(w http.ResponseWriter, r *http.Request) {
	cfg := g.Config()
	for _, rule := range cfg.Inbound {
		if rule.Path == r.URL.Path {
			g.handleInboundRule(w, r, rule)
			return
		}
	}
	http.NotFound(w, r)
}

func (g *Gateway) handleInboundRule(w http.ResponseWriter, r *http.Request, rule config.HTTPRuleConfig) {
	if rule.Method != "" && !strings.EqualFold(rule.Method, r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rule.AuthHeader == "" || rule.AuthToken == "" {
		http.Error(w, "inbound rule auth is required", http.StatusUnauthorized)
		return
	}
	if !authutil.ConstantTimeEqual(r.Header.Get(rule.AuthHeader), rule.AuthToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	values, err := requestValues(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if g.dispatcher != nil && rule.Fields["provider_id"] != "" && rule.Fields["status"] != "" {
		errCode, _ := strconv.Atoi(valueOf(values, rule.Fields["error_code"], "0"))
		err := g.dispatcher.HandleDLR(r.Context(), provider.DLR{
			Provider:   rule.Provider,
			ProviderID: valueOf(values, rule.Fields["provider_id"], ""),
			State:      valueOf(values, rule.Fields["status"], ""),
			ErrorCode:  errCode,
			DoneAt:     time.Now().UTC(),
		})
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		g.writeInboundSuccess(w, rule)
		return
	}
	msg := message.New(
		valueOf(values, rule.Fields["id"], newID()),
		message.DirectionMO,
		valueOf(values, rule.Fields["from"], ""),
		valueOf(values, rule.Fields["to"], ""),
		valueOf(values, rule.Fields["text"], ""),
	)
	msg.Provider = rule.Name
	msg.Segments = message.Split(msg.Text, message.SplitOptions{ForceEncoding: msg.Encoding})
	if err := g.store.SaveMessage(r.Context(), msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	g.writeInboundSuccess(w, rule)
}

func (g *Gateway) writeInboundSuccess(w http.ResponseWriter, rule config.HTTPRuleConfig) {
	status := rule.SuccessStatus
	if status == 0 {
		status = http.StatusOK
	}
	body := rule.SuccessBody
	if body == "" {
		body = `{"status":"accepted"}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func BuildOutboundRequest(ctx context.Context, endpoint string, rule config.HTTPRuleConfig, msg message.Message) (*http.Request, error) {
	return httprule.BuildOutboundRequest(ctx, endpoint, rule, msg)
}

func requestValues(r *http.Request) (map[string]string, error) {
	values := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var payload map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
			return nil, fmt.Errorf("invalid json")
		}
		for k, v := range payload {
			values[k] = fmt.Sprint(v)
		}
		return values, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	for k, v := range r.PostForm {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}
	return values, nil
}

func valueOf(values map[string]string, key, fallback string) string {
	if key == "" {
		return fallback
	}
	if v := values[key]; v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newID() string {
	return fmt.Sprintf("g%d", time.Now().UnixNano())
}

func intQuery(r *http.Request, name string, fallback int) int {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
