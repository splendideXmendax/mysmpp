package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
	"github.com/splendideXmendax/mysmpp/internal/smppclient"
)

// Runs the actual application entrypoint in an isolated child process. No live
// credentials, routes, database or provider are read by this test.
func TestGatewayHelperProcess(t *testing.T) {
	if os.Getenv("MYSMPP_TEST_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	flag.CommandLine = flag.NewFlagSet("mysmpp", flag.ExitOnError)
	os.Args = []string{"mysmpp", "-config", os.Getenv("MYSMPP_TEST_CONFIG")}
	main()
	os.Exit(0)
}

func TestGatewayWireQuotaLengthAndDelayedReceipt(t *testing.T) {
	freeAddr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		return addr
	}
	cfg := config.Default()
	cfg.Server.HTTPAddr = freeAddr()
	cfg.SMPP.Addr = freeAddr()
	cfg.Admin = config.AdminConfig{Username: "test-admin", Password: "test-only-admin"}
	cfg.SMPP.MaxSessions = 4
	cfg.SMPP.WindowSize = 4
	cfg.Dispatcher.Workers = 1
	cfg.Dispatcher.PerWorkerConcurrency = 2
	cfg.Storage = config.StorageConfig{Driver: "file", DSN: filepath.Join(t.TempDir(), "state.json")}
	cfg.Providers = []config.ProviderConfig{{Name: "local-mock", Protocol: "mock", Enabled: true}}
	cfg.Routes = []config.RouteConfig{{Name: "local-route", Provider: "local-mock"}}
	cfg.Tenants = []config.TenantConfig{{TenantID: "wire-test", Limits: config.TenantLimits{DailySegments: 1, Timezone: "UTC"}}}
	cfg.ESMEs = []config.ESMECred{{SystemID: "wire-test", Password: "qa-pass", TenantID: "wire-test"}}
	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestGatewayHelperProcess$")
	cmd.Env = append(os.Environ(), "MYSMPP_TEST_HELPER=1", "MYSMPP_TEST_CONFIG="+path)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + cfg.Server.HTTPAddr + "/healthz")
		if err == nil {
			resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated application did not become ready")
	}
	conn, err := net.DialTimeout("tcp", cfg.SMPP.Addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	body := append(smpp.CString("wire-test"), smpp.CString("qa-pass")...)
	body = append(body, smpp.CString("")...)
	body = append(body, 0x34, 0, 0, 0)
	if err = smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandBindTransceiver, SequenceID: 1, Body: body}); err != nil {
		t.Fatal(err)
	}
	bind, err := smpp.ReadPDU(conn)
	if err != nil || bind.Status != 0 {
		t.Fatalf("bind=%+v %v", bind, err)
	}
	m := smppclient.Message{SourceAddr: "brand", DestAddr: "8613800138000", Text: "hello", RegisteredDelivery: 1}
	pc := config.DefaultSMPPClientConfig()
	pc.RegisteredDelivery = 1
	send := func(seq uint32, msg smppclient.Message) {
		parts := smppclient.BuildSubmitSM(msg, pc)
		if len(parts) != 1 {
			t.Fatalf("parts=%d", len(parts))
		}
		if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: seq, Body: parts[0].Body}); err != nil {
			t.Fatal(err)
		}
	}
	send(2, m)
	first, err := smpp.ReadPDU(conn)
	if err != nil || first.CommandID != smpp.CommandSubmitSMResp || first.SequenceID != 2 || first.Status != 0 {
		t.Fatalf("initial submit=%+v %v", first, err)
	}
	send(3, m)
	m.SARSet = true
	m.SARRefNum = []byte{0, 1}
	m.SARTotalSegments = []byte{21}
	m.SARSegmentSeqnum = []byte{1}
	send(4, m)
	want := map[uint32]uint32{3: smpp.StatusThrottled, 4: 0x00000001}
	id := string(bytes.TrimRight(first.Body, "\x00"))
	gotDR := false
	for len(want) > 0 || !gotDR {
		pdu, err := smpp.ReadPDU(conn)
		if err != nil {
			t.Fatalf("missing submit response or delayed DR: %v", err)
		}
		switch pdu.CommandID {
		case smpp.CommandSubmitSMResp:
			status, ok := want[pdu.SequenceID]
			if !ok || pdu.Status != status {
				t.Fatalf("unexpected submit response %+v (want %x)", pdu, status)
			}
			if pdu.Status == 0 {
				id = string(bytes.TrimRight(pdu.Body, "\x00"))
			}
			delete(want, pdu.SequenceID)
		case smpp.CommandDeliverSM:
			if id == "" || !bytes.Contains(pdu.Body, []byte("id:"+id)) || !bytes.Contains(pdu.Body, []byte("stat:DELIVRD")) {
				t.Fatalf("wrong delayed receipt: %q", pdu.Body)
			}
			if err = smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, SequenceID: pdu.SequenceID}); err != nil {
				t.Fatal(err)
			}
			gotDR = true
		case smpp.CommandEnquireLink:
			smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandEnquireLinkResp, SequenceID: pdu.SequenceID})
		}
	}
	// Customer HTTP callback policy is still enforced at the real HTTP entry.
	req, _ := http.NewRequest(http.MethodPost, "http://"+cfg.Server.HTTPAddr+"/v1/messages", bytes.NewBufferString(`{"from":"brand","to":"+8613800138000","text":"hi","callback_url":"ftp://example.com/dlr"}`))
	req.SetBasicAuth(cfg.Admin.Username, cfg.Admin.Password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported callback scheme accepted: %d", resp.StatusCode)
	}
}
