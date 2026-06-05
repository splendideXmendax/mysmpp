package provider

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestHTTPProviderSendsRenderedRequest(t *testing.T) {
	var gotPath, gotBody, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Request-ID")
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messageId":"up-1"}`))
	}))
	defer server.Close()

	p := NewHTTPProvider(config.ProviderConfig{
		Name:     "http-a",
		Endpoint: server.URL + "/send",
	}, config.HTTPRuleConfig{
		Method:      "POST",
		ContentType: "application/json",
		Response:    config.ResponseParseConfig{IDPath: "messageId"},
		Fields: map[string]string{
			"messageId": "id",
			"mobile":    "to",
			"msg":       "text",
		},
		Headers: map[string]string{"X-Request-ID": "{{id}}"},
	})
	defer p.Close()

	id, err := p.Send(OutboundMessage{GatewayID: "g1", SourceAddr: "1069", DestAddr: "13800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "up-1" {
		t.Fatalf("unexpected provider id %q", id)
	}
	if gotPath != "/send" || gotHeader != "g1" {
		t.Fatalf("request not rendered correctly path=%q header=%q", gotPath, gotHeader)
	}
	if gotBody == "" || gotBody == "{}" {
		t.Fatalf("body not rendered: %q", gotBody)
	}
}

func TestHTTPProviderExtractsProviderIDFromArrayPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"messageId":"up-1"}]}`))
	}))
	defer server.Close()

	p := NewHTTPProvider(config.ProviderConfig{
		Name:     "http-a",
		Endpoint: server.URL,
	}, config.HTTPRuleConfig{
		Method:   "POST",
		Response: config.ResponseParseConfig{IDPath: "data.0.messageId"},
		Fields:   map[string]string{"mobile": "to", "msg": "text"},
	})
	defer p.Close()

	id, err := p.Send(OutboundMessage{GatewayID: "g1", DestAddr: "13800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "up-1" {
		t.Fatalf("unexpected provider id %q", id)
	}
}

func TestHTTPProviderUsesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"messageId":"up-1"}`))
	}))
	defer server.Close()

	p := NewHTTPProvider(config.ProviderConfig{
		Name:          "http-a",
		Endpoint:      server.URL,
		HTTPTimeoutMS: 10,
	}, config.HTTPRuleConfig{
		Method:   "POST",
		Response: config.ResponseParseConfig{IDPath: "messageId"},
		Fields:   map[string]string{"mobile": "to", "msg": "text"},
	})
	defer p.Close()

	if _, err := p.Send(OutboundMessage{GatewayID: "g1", DestAddr: "13800138000", Text: "hello"}); err == nil {
		t.Fatal("expected configured timeout")
	}
}

func TestHTTPProviderExtractsProviderIDWithRegex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK MsgID: abc123 accepted"))
	}))
	defer server.Close()

	p := NewHTTPProvider(config.ProviderConfig{
		Name:     "http-a",
		Endpoint: server.URL,
	}, config.HTTPRuleConfig{
		Method:      "POST",
		ContentType: "application/x-www-form-urlencoded",
		Fields: map[string]string{
			"mobile":              "to",
			"msg":                 "text",
			"__response_id_regex": `MsgID:\s+([A-Za-z0-9_-]+)`,
		},
	})
	defer p.Close()

	id, err := p.Send(OutboundMessage{GatewayID: "g1", DestAddr: "13800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc123" {
		t.Fatalf("unexpected provider id %q", id)
	}
}

func TestHTTPProviderDoesNotUseWholeBodyWhenPathMisses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	p := NewHTTPProvider(config.ProviderConfig{
		Name:     "http-a",
		Endpoint: server.URL,
	}, config.HTTPRuleConfig{
		Method:      "POST",
		ContentType: "application/x-www-form-urlencoded",
		Fields: map[string]string{
			"mobile":        "to",
			"msg":           "text",
			"__response_id": "msgid",
		},
	})
	defer p.Close()

	id, err := p.Send(OutboundMessage{GatewayID: "g1", DestAddr: "13800138000", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "g1" {
		t.Fatalf("expected gateway id fallback, got %q", id)
	}
}
