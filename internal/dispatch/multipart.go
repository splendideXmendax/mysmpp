package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/router"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

func digestJSON(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func multipartIdentity(env Envelope) (key string, total, part int) {
	info, ok := smpp.ParseConcat(env.UDH)
	kind := ""
	if ok {
		// Include every non-concatenation IE (ports, language tables, etc.) but
		// remove the sequence number. UDH8 and UDH16 remain distinct namespaces.
		udh := append([]byte(nil), env.UDH...)
		for offset := 1; offset+1 < len(udh); {
			length := int(udh[offset+1])
			end := offset + 2 + length
			if end > len(udh) {
				break
			}
			if udh[offset] == 0 || udh[offset] == 8 {
				udh[end-2] = 0
				udh[end-1] = 0
			}
			offset = end
		}
		kind = "udh:" + hex.EncodeToString(udh)
	}
	if env.SARSet && len(env.SARTotalSegments) == 1 && len(env.SARSegmentSeqnum) == 1 {
		ok = true
		info.Total = env.SARTotalSegments[0]
		info.Part = env.SARSegmentSeqnum[0]
		kind = "sar:" + hex.EncodeToString(env.SARRefNum)
	}
	if !ok || info.Total <= 1 {
		return "", 0, 0
	}
	// Total is checked against the stored binding instead of making a new key:
	// changing total mid-message must not create a second route assignment.
	key = digestJSON([]any{env.TenantID, env.AccountID, env.ClientID, env.Source.SMPPSystemID, env.From, env.To, env.DataCoding, kind})
	return key, int(info.Total), int(info.Part)
}

func (d *Dispatcher) pinMultipart(ctx context.Context, env Envelope, rt *router.Router, route config.RouteConfig, routeFound bool) (config.RouteConfig, string, bool, error) {
	key, total, part := multipartIdentity(env)
	if key == "" {
		return route, "", routeFound, nil
	}
	proposed := store.MultipartBinding{Key: key, Total: total, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if routeFound {
		if match, ok := rt.SelectProvider(route, key); ok {
			proposed.Route, _ = json.Marshal(route)
			proposed.Provider = match.Provider
		}
	}
	fp := digestJSON([]any{env.RawPayloadSet, env.RawPayload, env.Text, env.RegisteredDelivery})
	b, err := d.store.BindMultipart(ctx, proposed, part, fp)
	if err != nil {
		return route, "", false, err
	}
	if err = json.Unmarshal(b.Route, &route); err != nil {
		return route, "", false, err
	}
	if !rt.BindingEnabled(route.Name, b.Provider) {
		return route, "", false, fmt.Errorf("%w: multipart route or provider disabled", ErrNoRoute)
	}
	return route, b.Provider, true, nil
}
