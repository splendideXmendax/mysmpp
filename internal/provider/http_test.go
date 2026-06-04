package provider

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
