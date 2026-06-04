package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

type HTTPProvider struct {
	name            string
	endpoint        string
	rule            config.HTTPRuleConfig
	client          *http.Client
	providerIDField string
}

func NewHTTPProvider(p config.ProviderConfig, rule config.HTTPRuleConfig) *HTTPProvider {
	return &HTTPProvider{
		name:     p.Name,
		endpoint: p.Endpoint,
		rule:     rule,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		providerIDField: firstNonEmpty(rule.Fields["__response_id_path"], firstNonEmpty(rule.Fields["__response_id"], "messageId")),
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
	req, err := buildOutboundRequest(ctx, p.endpoint, p.rule, m)
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
	id := extractProviderID(body, resp.Header.Get("Content-Type"), p.providerIDField, p.rule.Fields["__response_id_regex"])
	if id == "" {
		id = msg.GatewayID
	}
	return id, nil
}

func buildOutboundRequest(ctx context.Context, endpoint string, rule config.HTTPRuleConfig, msg message.Message) (*http.Request, error) {
	values := url.Values{}
	for target, source := range rule.Fields {
		if strings.HasPrefix(target, "__") {
			continue
		}
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
	if strings.Contains(contentType, "application/json") || strings.HasPrefix(value, "{") {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if raw, ok := valueAtPath(payload, field); ok {
				return strings.TrimSpace(fmt.Sprint(raw))
			}
			if raw, ok := payload["id"]; ok {
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
	if field != "" && field != "messageId" {
		return ""
	}
	return value
}

func valueAtPath(payload map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
