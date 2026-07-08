package provider

import (
	"context"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type multiIDTestProvider struct {
	ids []string
}

func (p multiIDTestProvider) Send(OutboundMessage) (string, error) {
	if len(p.ids) == 0 {
		return "", nil
	}
	return p.ids[0], nil
}

func (p multiIDTestProvider) SendAll(OutboundMessage) ([]string, error) {
	return append([]string(nil), p.ids...), nil
}

func (p multiIDTestProvider) OnDLR(DLRCallback) {}

func TestRateLimitedProviderPreservesSendAll(t *testing.T) {
	wrapped := NewRateLimitedProvider(multiIDTestProvider{
		ids: []string{"p1", "p2", "p3"},
	}, config.ProviderRateLimit{TPS: 1, Burst: 1, TimeoutMS: 100})

	multi, ok := wrapped.(MultiIDProvider)
	if !ok {
		t.Fatal("rate limited provider should preserve MultiIDProvider")
	}
	ids, err := multi.SendAll(OutboundMessage{Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "p1" || ids[1] != "p2" || ids[2] != "p3" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}
