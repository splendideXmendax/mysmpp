package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:29175", "gateway address")
	user := flag.String("u", "dev-esme", "system_id")
	pass := flag.String("p", "mysmpp-esme-29175", "password")
	src := flag.String("src", "10690000", "source address")
	dst := flag.String("dst", "13800138000", "destination address")
	text := flag.String("text", "ping", "message text")
	count := flag.Int("n", 1, "message count")
	wait := flag.Duration("wait", 15*time.Second, "wait time for DLR")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandBindTransceiver, SequenceID: 1, Body: bindBody(*user, *pass)}); err != nil {
		log.Fatal(err)
	}
	resp, err := smpp.ReadPDU(conn)
	if err != nil {
		log.Fatal(err)
	}
	if resp.Status != 0 {
		log.Fatalf("bind failed status=0x%08x", resp.Status)
	}
	fmt.Println("bound. sending...")

	for i := 0; i < *count; i++ {
		seq := uint32(i + 2)
		if err := smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandSubmitSM, SequenceID: seq, Body: submitBody(*src, *dst, *text, 0x01)}); err != nil {
			log.Fatal(err)
		}
		resp, err := smpp.ReadPDU(conn)
		if err != nil {
			log.Fatal(err)
		}
		if resp.Status != 0 {
			log.Fatalf("submit failed status=0x%08x", resp.Status)
		}
		offset := 0
		fmt.Printf("submitted msg_id=%s\n", readCString(resp.Body, &offset))
	}

	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		pdu, err := smpp.ReadPDU(conn)
		if err != nil {
			return
		}
		if pdu.CommandID != smpp.CommandDeliverSM {
			continue
		}
		dlr := parseDeliverSM(pdu.Body)
		fmt.Printf("[DLR] %s -> %s : %s\n", dlr.from, dlr.to, dlr.text)
		_ = smpp.WritePDU(conn, smpp.PDU{CommandID: smpp.CommandDeliverSMResp, SequenceID: pdu.SequenceID})
	}
}

func bindBody(systemID, password string) []byte {
	body := []byte{}
	body = append(body, smpp.CString(systemID)...)
	body = append(body, smpp.CString(password)...)
	body = append(body, smpp.CString("gateway")...)
	body = append(body, 0x34, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	return body
}

func submitBody(from, to, text string, registeredDelivery byte) []byte {
	dataCoding := byte(0x00)
	encoded := message.EncodeText(text, dataCoding)
	if message.DetectEncoding(text) == "ucs2" {
		dataCoding = 0x08
		encoded = message.EncodeText(text, dataCoding)
	}
	body := []byte{}
	body = append(body, smpp.CString("")...)
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString(from)...)
	body = append(body, 0x01, 0x01)
	body = append(body, smpp.CString(to)...)
	body = append(body, 0x00, 0x00, 0x00)
	body = append(body, smpp.CString("")...)
	body = append(body, smpp.CString("")...)
	body = append(body, registeredDelivery, 0x00, dataCoding, 0x00)
	body = append(body, byte(len(encoded)))
	body = append(body, encoded...)
	return body
}

type deliverSM struct {
	from string
	to   string
	text string
}

func parseDeliverSM(body []byte) deliverSM {
	offset := 0
	_ = readCString(body, &offset)
	offset += 2
	from := readCString(body, &offset)
	offset += 2
	to := readCString(body, &offset)
	offset += 3
	_ = readCString(body, &offset)
	_ = readCString(body, &offset)
	offset += 4
	smLen := int(body[offset])
	offset++
	return deliverSM{from: from, to: to, text: string(body[offset : offset+smLen])}
}

func readCString(body []byte, offset *int) string {
	start := *offset
	for *offset < len(body) && body[*offset] != 0 {
		*offset++
	}
	value := string(body[start:*offset])
	if *offset < len(body) {
		*offset++
	}
	return value
}
