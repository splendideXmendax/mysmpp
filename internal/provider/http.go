package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/httprule"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type HTTPProvider struct {
	name            string
	endpoint        string
	rule            config.HTTPRuleConfig
	client          *http.Client
	providerIDField string
	providerIDRegex string
}

func NewHTTPProvider(p config.ProviderConfig, rule config.HTTPRuleConfig) *HTTPProvider {
	timeout := time.Duration(p.HTTPTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &HTTPProvider{
		name:     p.Name,
		endpoint: p.Endpoint,
		rule:     rule,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 128,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		providerIDField: firstNonEmpty(rule.Response.IDPath, firstNonEmpty(rule.Fields["__response_id_path"], rule.Fields["__response_id"])),
		providerIDRegex: firstNonEmpty(rule.Response.IDRegex, rule.Fields["__response_id_regex"]),
	}
}

func (p *HTTPProvider) Name() string { return p.name }

func (p *HTTPProvider) Send(msg OutboundMessage) (string, error) {
	ctx := msg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	m := message.Message{
		ID:       msg.GatewayID,
		From:     msg.SourceAddr,
		To:       msg.DestAddr,
		Text:     msg.Text,
		Encoding: firstNonEmpty(msg.Encoding, encodingName(msg.DataCoding)),
		Metadata: msg.Meta,
	}
	req, err := httprule.BuildOutboundRequest(ctx, p.endpoint, p.rule, m)
	if err != nil {
		return "", err
	}
	if p.rule.AuthHeader != "" && p.rule.AuthToken != "" {
		req.Header.Set(p.rule.AuthHeader, p.rule.AuthToken)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("provider %s status %d: %s", p.name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	id := extractProviderID(body, resp.Header.Get("Content-Type"), p.providerIDField, p.providerIDRegex)
	if id == "" {
		id = msg.GatewayID
	}
	return id, nil
}

func (p *HTTPProvider) OnDLR(DLRCallback) {}

func (p *HTTPProvider) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func encodingName(dataCoding uint8) string {
	if dataCoding == 0x08 {
		return "ucs2"
	}
	return "gsm7"
}

func extractProviderID(body []byte, contentType, field, pattern string) string {
	value := strings.TrimSpace(string(body))
	if value == "" {
		return ""
	}
	if (strings.Contains(contentType, "application/json") || strings.HasPrefix(value, "{")) && field != "" {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if raw, ok := valueAtPath(payload, field); ok {
				return strings.TrimSpace(fmt.Sprint(raw))
			}
		}
	}
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return ""
		}
		matches := re.FindStringSubmatch(value)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
		if len(matches) == 1 {
			return strings.TrimSpace(matches[0])
		}
		return ""
	}
	return ""
}

func valueAtPath(payload map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		switch obj := current.(type) {
		case map[string]any:
			next, ok := obj[part]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(obj) {
				return nil, false
			}
			current = obj[idx]
		default:
			return nil, false
		}
	}
	return current, true
}
