package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/store"
)

func finalTestDispatcher(st store.Store, bodies *[][]byte) *Dispatcher {
	return &Dispatcher{store: st, logger: slog.Default(), httpClient: &http.Client{Transport: forwardTestTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		*bodies = append(*bodies, b)
		return &http.Response{StatusCode: 204, Body: http.NoBody}, err
	})}}
}

func drainFinalTestJobs(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	for n := 0; n < 100; n++ {
		j, ok, err := d.store.ClaimReceipt(ctx, "delivery", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
		if err = d.deliverReceipt(ctx, j); err != nil {
			t.Fatal(err)
		}
		if err = d.store.FinishReceipt(ctx, j, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("delivery queue did not drain")
}

func assertFinalTestBody(t *testing.T, bodies [][]byte, state string, code, segments int) {
	t.Helper()
	if len(bodies) != 1 {
		t.Fatalf("want one HTTP notification, got %d", len(bodies))
	}
	var body map[string]any
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	assertCallbackRoutingPrivate(t, body)
	if body["state"] != state || body["message_state"] != state || body["error_code"] != float64(code) || body["final"] != true || body["segment_count"] != float64(segments) || body["segment_index"] != nil {
		t.Fatalf("incorrect aggregate: %s", bodies[0])
	}
}

func TestHTTPFinalReceiptConcurrentPartsAndInterim(t *testing.T) {
	testHTTPFinalConcurrent(t, store.NewMemory())
}

func testHTTPFinalConcurrent(t *testing.T, st store.Store) {
	ctx := context.Background()
	if err := st.SaveMessage(ctx, testMessage("parallel")); err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	d := finalTestDispatcher(st, &bodies)
	sink := &captureCDR{}
	d.SetCDRSink(sink)
	const total = 20
	for i := 1; i <= total; i++ {
		if err := st.SavePending(ctx, store.Pending{GatewayID: "parallel", Provider: "private", ProviderID: fmt.Sprint(i), SourceKind: "http", CallbackURL: "http://callback.example/dr", SegmentIndex: i, SegmentCount: total, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PrepareReceipt(ctx, store.ReceiptEvent{Provider: "private", ProviderID: "1", State: "ENROUTE", DoneAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	drainFinalTestJobs(t, d)
	if len(bodies) != 0 {
		t.Fatal("interim HTTP notification was sent")
	}
	var wg sync.WaitGroup
	for i := 1; i <= total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state, code := "DELIVRD", 0
			if i == 7 {
				state, code = "UNDELIV", 42
			}
			if err := st.PrepareReceipt(ctx, store.ReceiptEvent{Provider: "private", ProviderID: fmt.Sprint(i), State: state, ErrorCode: code, DoneAt: time.Now()}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	drainFinalTestJobs(t, d)
	assertFinalTestBody(t, bodies, "UNDELIV", 42, total)
	if sink.count("dlr") != total+1 {
		t.Fatalf("internal per-part CDR changed: %d", sink.count("dlr"))
	}
	// A duplicate source event must not schedule another final notification.
	if err := st.PrepareReceipt(ctx, store.ReceiptEvent{Provider: "private", ProviderID: "1", State: "DELIVRD", DoneAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	drainFinalTestJobs(t, d)
	assertFinalTestBody(t, bodies, "UNDELIV", 42, total)
}

func TestHTTPFinalReceiptBatchFailuresAndExpiry(t *testing.T) {
	testHTTPFinalBatches(t, store.NewMemory())
}

func testHTTPFinalBatches(t *testing.T, st store.Store) {
	for _, mode := range []string{"submit-rejected", "expired-missing-part"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var bodies [][]byte
			d := finalTestDispatcher(st, &bodies)
			m := testMessage(mode)
			id, _, _, err := st.SubmitAtomic(ctx, m, store.OutboxItem{GatewayID: m.ID, Provider: "p"}, store.SubmitOptions{})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := st.ClaimOutbox(ctx, "w", 1)
			if err != nil || len(claimed) != 1 || claimed[0].ID != id {
				t.Fatalf("claim: %v %v", claimed, err)
			}
			if err = st.MarkOutboxSending(ctx, id, "w"); err != nil {
				t.Fatal(err)
			}
			var rows []store.Pending
			for i := 1; i <= 3; i++ {
				p := store.Pending{GatewayID: m.ID, Provider: "p", ProviderID: mode + fmt.Sprint(i), Text: "a\x00b", SourceKind: "http", CallbackURL: "http://callback.example/dr", SegmentIndex: i, SegmentCount: 3, ReliabilityManaged: true, ExpiresAt: time.Now().Add(-time.Second)}
				if mode == "submit-rejected" {
					p.DLRReady, p.DLRState, p.DLRErrorCode, p.DLRDoneAt = true, "REJECTD", 88, time.Now().UTC()
				} else if i == 3 {
					continue // expiry must synthesize the missing part too
				}
				rows = append(rows, p)
			}
			if err = st.CompleteOutboxSend(ctx, id, "w", rows); err != nil {
				t.Fatal(err)
			}
			if mode != "submit-rejected" {
				if _, err = st.SweepExpiredPending(ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				if left, err := st.ListPendingByGatewayID(ctx, m.ID); err != nil || len(left) != 0 {
					t.Fatalf("expiry must remove pending before delivery: %v %v", left, err)
				}
			}
			drainFinalTestJobs(t, d)
			state, code := "REJECTD", 88
			if mode != "submit-rejected" {
				state, code = "EXPIRED", 1
			}
			assertFinalTestBody(t, bodies, state, code, 3)
		})
	}
}

func TestHTTPFinalReceiptRestartAndHistoricalFinalSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "receipts.json")
	st, err := store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	d := finalTestDispatcher(st, &bodies)
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Old per-part final snapshots have no HTTPFinal field. The last
			// segment can succeed while the aggregate failed on another part.
			v := store.ReceiptDelivery{Pending: store.Pending{GatewayID: "upgrade", Provider: "p", ProviderID: fmt.Sprint(i), SourceKind: "http", Text: "a\x00b", CallbackURL: "https://callback.example/dr", SegmentCount: 3, SegmentIndex: i}, Event: store.ReceiptEvent{State: "DELIVRD", DoneAt: time.Now().UTC()}, Aggregate: store.ReceiptAggregate{Final: true, State: "UNDELIV", ErrorCode: 42}}
			raw, _ := json.Marshal(v)
			if err := d.deliverReceipt(ctx, store.ReceiptJob{Payload: raw, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(bodies) != 0 {
		t.Fatal("source snapshot directly sent an HTTP callback")
	}
	st, err = store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d.store = st
	j, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now())
	if err != nil || !ok {
		t.Fatalf("aggregate not persisted: %v %v", ok, err)
	}
	// Simulate a lost acknowledgement: receiver accepted the callback, then
	// process died before FinishReceipt. Recovery must reuse ID and body.
	if err = d.deliverReceipt(ctx, j); err != nil {
		t.Fatal(err)
	}
	st, err = store.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d.store = st
	retry, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now().Add(2*time.Minute))
	if err != nil || !ok {
		t.Fatalf("lease not recovered: %v %v", ok, err)
	}
	if err = d.deliverReceipt(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if err = st.FinishReceipt(ctx, j, time.Time{}, ""); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("stale worker committed: %v", err)
	}
	if err = st.FinishReceipt(ctx, retry, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	drainFinalTestJobs(t, d)
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry changed payload or duplicated jobs: %q", bodies)
	}
	assertFinalTestBody(t, bodies[:1], "UNDELIV", 42, 3)
}
