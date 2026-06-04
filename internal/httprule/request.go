package httprule

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
)

func BuildOutboundRequest(ctx context.Context, endpoint string, rule config.HTTPRuleConfig, msg message.Message) (*http.Request, error) {
	values := url.Values{}
	for target, source := range rule.Fields {
		if strings.HasPrefix(target, "__") {
			continue
		}
		values.Set(target, MessageField(msg, source))
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
		req.Header.Set(k, ExpandMessage(v, msg))
	}
	return req, nil
}

func MessageField(msg message.Message, field string) string {
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

func ExpandMessage(template string, msg message.Message) string {
	replacer := strings.NewReplacer(
		"{{id}}", msg.ID,
		"{{from}}", msg.From,
		"{{to}}", msg.To,
		"{{text}}", msg.Text,
		"{{encoding}}", msg.Encoding,
	)
	return replacer.Replace(template)
}
