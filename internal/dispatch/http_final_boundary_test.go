package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/store"
)

// Model both a failed write and a committed write whose acknowledgement was
// lost. Retrying either boundary must converge to one gateway notification.
type finalReceiptFaultStore struct {
	store.Store
	mode  string
	fired bool
	err   error
}

func (s *finalReceiptFaultStore) EnqueueReceipt(ctx context.Context, job store.ReceiptJob) error {
	if s.fired || !strings.HasPrefix(job.ID, "http-final:") || s.mode == "mark" {
		return s.Store.EnqueueReceipt(ctx, job)
	}
	s.fired = true
	if s.mode == "after-commit" {
		if err := s.Store.EnqueueReceipt(ctx, job); err != nil {
			return err
		}
	}
	return s.err
}

func (s *finalReceiptFaultStore) MarkReceiptDelivered(ctx context.Context, rec store.Pending) error {
	if s.mode == "mark" && !s.fired {
		s.fired = true
		return s.err
	}
	return s.Store.MarkReceiptDelivered(ctx, rec)
}

func TestHTTPFinalReceiptFailureBoundaries(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testHTTPFinalFailureBoundaries(t, store.NewMemory()) })
	t.Run("file", func(t *testing.T) {
		st, err := store.NewFile(filepath.Join(t.TempDir(), "receipts.json"))
		if err != nil {
			t.Fatal(err)
		}
		testHTTPFinalFailureBoundaries(t, st)
	})
}

func testHTTPFinalFailureBoundaries(t *testing.T, st store.Store) {
	t.Helper()
	for _, mode := range []string{"before-commit", "after-commit", "mark"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			id := "http-boundary-" + mode
			if err := st.SaveMessage(ctx, testMessage(id)); err != nil {
				t.Fatal(err)
			}
			p := store.Pending{GatewayID: id, Provider: "internal", ProviderID: id, Route: "internal-route", SourceKind: "http", CallbackURL: "https://callback.example/dr", SegmentIndex: 1, SegmentCount: 1, ExpiresAt: time.Now().Add(time.Hour)}
			if err := st.SavePending(ctx, p); err != nil {
				t.Fatal(err)
			}
			if err := st.PrepareReceipt(ctx, store.ReceiptEvent{Provider: p.Provider, ProviderID: p.ProviderID, State: "DELIVRD", DoneAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			job, ok, err := st.ClaimReceipt(ctx, "delivery", time.Now().UTC())
			if err != nil || !ok {
				t.Fatalf("source job unavailable: %v %v", ok, err)
			}
			fault := &finalReceiptFaultStore{Store: st, mode: mode, err: errors.New("injected storage failure")}
			var bodies [][]byte
			d := finalTestDispatcher(fault, &bodies)
			sink := &captureCDR{}
			d.SetCDRSink(sink)
			if err = d.deliverReceipt(ctx, job); !errors.Is(err, fault.err) {
				t.Fatalf("source acknowledged despite persistence failure: %v", err)
			}
			pending, ok, err := st.GetPending(ctx, p.Provider, p.ProviderID)
			if err != nil || !ok || pending.DLRDelivered || len(bodies) != 0 || sink.count("dlr") != 0 {
				t.Fatalf("source completed prematurely: pending=%+v bodies=%d err=%v", pending, len(bodies), err)
			}
			if mode != "before-commit" {
				// The final notification may succeed while its source job is
				// still claimed. Retrying the source must retain the done marker.
				drainFinalTestJobs(t, d)
				assertFinalTestBody(t, bodies, "DELIVRD", 0, 1)
			}
			if err = st.FinishReceipt(ctx, job, time.Now().UTC(), fault.err.Error()); err != nil {
				t.Fatal(err)
			}
			drainFinalTestJobs(t, d)
			assertFinalTestBody(t, bodies, "DELIVRD", 0, 1)
			pending, ok, err = st.GetPending(ctx, p.Provider, p.ProviderID)
			if err != nil || !ok || !pending.DLRDelivered || pending.Provider != p.Provider || pending.Route != p.Route || sink.count("dlr") != 1 {
				t.Fatalf("source did not converge: pending=%+v CDR=%d err=%v", pending, sink.count("dlr"), err)
			}
			counts, err := st.ReceiptCounts(ctx)
			if err != nil || counts["delivery:pending"] != 0 || counts["delivery:claimed"] != 0 {
				t.Fatalf("unfinished delivery jobs: %v %v", counts, err)
			}
		})
	}
}
