package httpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type Gateway struct {
	mu    sync.RWMutex
	cfg   config.Config
	store store.Store
	mux   *http.ServeMux
}

func New(cfg config.Config, st store.Store) *Gateway {
	g := &Gateway{cfg: cfg, store: st, mux: http.NewServeMux()}
	g.routes()
	return g
}

func (g *Gateway) Handler() http.Handler {
	return g.mux
}

func (g *Gateway) routes() {
	g.mux.HandleFunc("/healthz", g.health)
	g.mux.HandleFunc("/v1/messages", g.messages)
	g.mux.HandleFunc("/v1/config", g.configAPI)
	g.mux.HandleFunc("/ui/config", g.configPage)
	g.mux.HandleFunc("/", g.dynamicInbound)
}

func (g *Gateway) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (g *Gateway) messages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		messages, err := g.store.ListMessages(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, messages)
	case http.MethodPost:
		var req struct {
			From string            `json:"from"`
			To   string            `json:"to"`
			Text string            `json:"text"`
			Meta map[string]string `json:"meta"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
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
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = cfg
	return nil
}

func (g *Gateway) configAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, g.Config())
	case http.MethodPut, http.MethodPost:
		var cfg config.Config
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
	if rule.AuthHeader != "" && r.Header.Get(rule.AuthHeader) != rule.AuthToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	values, err := requestValues(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	values := url.Values{}
	for target, source := range rule.Fields {
		values.Set(target, messageField(msg, source))
	}

	method := strings.ToUpper(rule.Method)
	if method == "" {
		method = http.MethodPost
	}
	contentType := rule.ContentType
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}

	var body io.Reader
	requestURL := endpoint
	if method == http.MethodGet {
		sep := "?"
		if strings.Contains(requestURL, "?") {
			sep = "&"
		}
		requestURL += sep + values.Encode()
	} else if contentType == "application/json" {
		payload := map[string]string{}
		for k := range values {
			payload[k] = values.Get(k)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	} else {
		body = strings.NewReader(values.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range rule.Headers {
		req.Header.Set(k, expandMessage(v, msg))
	}
	return req, nil
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
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
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

func messageField(msg message.Message, field string) string {
	switch field {
	case "id":
		return msg.ID
	case "from":
		return msg.From
	case "to":
		return msg.To
	case "text":
		return msg.Text
	case "encoding":
		return msg.Encoding
	default:
		return msg.Metadata[field]
	}
}

func expandMessage(template string, msg message.Message) string {
	replacer := strings.NewReplacer(
		"{{id}}", msg.ID,
		"{{from}}", msg.From,
		"{{to}}", msg.To,
		"{{text}}", msg.Text,
		"{{encoding}}", msg.Encoding,
	)
	return replacer.Replace(template)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
