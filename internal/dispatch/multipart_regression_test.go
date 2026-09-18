package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/provider"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func TestMultipartProtocolsKeepRouteAcrossOutOfOrderSessions(t *testing.T) {
	for _, protocol := range []string{"udh8", "udh16", "sar"} {
		t.Run(protocol, func(t *testing.T) {
			cfg := testDispatcherConfig()
			cfg.PollIntervalMS = 60000
			d := New(nil, provider.NewRegistry(), nil, cfg, store.NewMemory())
			defer d.Close()
			providers := []config.ProviderConfig{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}}
			route := config.RouteConfig{Name: "original", Priority: 1, Weighted: []config.WeightedProvider{{Provider: "a", Weight: 50}, {Provider: "b", Weight: 50}}}
			d.ReloadRoutes([]config.RouteConfig{route}, providers)
			env := Envelope{From: "brand", To: "8613800138000", ClientID: "smpp:account", RawPayloadSet: true, Source: SubmitSource{Kind: SourceSMPP, SMPPSystemID: "account"}}
			var first Receipt
			for i, part := range []byte{2, 3, 1} {
				env.Source.SMPPSessionID = fmt.Sprintf("session-%d", part)
				env.Text = fmt.Sprintf("part-%d", part)
				env.RawPayload = []byte(env.Text)
				switch protocol {
				case "udh8":
					env.UDH = []byte{5, 0, 3, 99, 3, part}
				case "udh16":
					env.UDH = []byte{6, 8, 4, 1, 99, 3, part}
				case "sar":
					env.SARSet = true
					env.SARRefNum = []byte{1, 99}
					env.SARTotalSegments = []byte{3}
					env.SARSegmentSeqnum = []byte{part}
				}
				got, err := d.Submit(context.Background(), env)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = got
					other := "a"
					if first.Provider == "a" {
						other = "b"
					}
					d.ReloadRoutes([]config.RouteConfig{route, {Name: "new-priority", Priority: 100, Provider: other}}, providers)
				} else if got.Provider != first.Provider || got.Route != first.Route || got.GatewayID == first.GatewayID {
					t.Fatalf("split routing across sessions: first=%+v next=%+v", first, got)
				}
			}
			d.ReloadRoutes([]config.RouteConfig{{Name: "replacement", Provider: "b"}}, providers)
			if _, err := d.Submit(context.Background(), env); !errors.Is(err, ErrNoRoute) {
				t.Fatalf("removed route must not move multipart group: %v", err)
			}
		})
	}
}
