package smpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/store"
)

type Server struct {
	cfg      config.SMPPConfig
	store    store.Store
	logger   *slog.Logger
	listener net.Listener
	wg       sync.WaitGroup
}

func NewServer(cfg config.SMPPConfig, st store.Store, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, logger: logger}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.logger.Info("smpp listening", "addr", s.cfg.Addr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleSession(ctx, conn)
		}()
	}
}

func (s *Server) handleSession(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	s.logger.Info("smpp session opened", "remote", remote)
	defer s.logger.Info("smpp session closed", "remote", remote)

	bound := false
	for {
		pdu, err := ReadPDU(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Warn("read smpp pdu failed", "remote", remote, "err", err)
			}
			return
		}

		s.logger.Debug("smpp pdu", "remote", remote, "command", commandName(pdu.CommandID), "sequence", pdu.SequenceID)
		switch pdu.CommandID {
		case commandBindReceiver, commandBindTransmitter, commandBindTransceiver:
			bound = s.handleBind(conn, pdu)
		case commandEnquireLink:
			_ = WritePDU(conn, PDU{CommandID: commandEnquireLinkResp, Status: statusOK, SequenceID: pdu.SequenceID})
		case commandUnbind:
			_ = WritePDU(conn, PDU{CommandID: commandUnbindResp, Status: statusOK, SequenceID: pdu.SequenceID})
			return
		case commandSubmitSM:
			if !bound {
				_ = WritePDU(conn, PDU{CommandID: commandSubmitSMResp, Status: statusInvalidBind, SequenceID: pdu.SequenceID})
				continue
			}
			msg, err := parseSubmitSM(pdu)
			if err != nil {
				s.logger.Warn("submit_sm parse failed", "remote", remote, "err", err)
				_ = WritePDU(conn, PDU{CommandID: commandSubmitSMResp, Status: statusInvalidCmd, SequenceID: pdu.SequenceID})
				continue
			}
			msg.ID = fmt.Sprintf("smpp-%d", pdu.SequenceID)
			msg.Direction = message.DirectionMO
			msg.Provider = remote
			msg.Segments = message.Split(msg.Text, message.SplitOptions{ForceEncoding: msg.Encoding})
			if err := s.store.SaveMessage(ctx, msg); err != nil {
				s.logger.Error("save submit_sm failed", "remote", remote, "err", err)
			}
			_ = WritePDU(conn, PDU{CommandID: commandSubmitSMResp, Status: statusOK, SequenceID: pdu.SequenceID, Body: CString(msg.ID)})
		default:
			_ = WritePDU(conn, PDU{CommandID: commandGenericNack, Status: statusInvalidCmd, SequenceID: pdu.SequenceID})
		}
	}
}

func (s *Server) handleBind(conn net.Conn, pdu PDU) bool {
	offset := 0
	systemID := readCString(pdu.Body, &offset)
	password := readCString(pdu.Body, &offset)
	status := statusOK
	if s.cfg.SystemID != "" && systemID != s.cfg.SystemID {
		status = statusInvalidBind
	}
	if s.cfg.Password != "" && password != s.cfg.Password {
		status = statusInvalidBind
	}

	respID := commandBindTransceiverResp
	switch pdu.CommandID {
	case commandBindReceiver:
		respID = commandBindReceiverResp
	case commandBindTransmitter:
		respID = commandBindTransmitterResp
	}
	body := CString(s.cfg.SystemID)
	_ = WritePDU(conn, PDU{CommandID: respID, Status: status, SequenceID: pdu.SequenceID, Body: body})
	return status == statusOK
}

func parseSubmitSM(pdu PDU) (message.Message, error) {
	offset := 0
	_ = readCString(pdu.Body, &offset) // service_type
	if offset+2 > len(pdu.Body) {
		return message.Message{}, errors.New("short submit_sm")
	}
	offset += 2 // source_addr_ton, source_addr_npi
	from := readCString(pdu.Body, &offset)
	if offset+2 > len(pdu.Body) {
		return message.Message{}, errors.New("missing destination ton/npi")
	}
	offset += 2
	to := readCString(pdu.Body, &offset)
	if offset+3 > len(pdu.Body) {
		return message.Message{}, errors.New("missing submit_sm fixed fields")
	}
	offset += 3 // esm_class, protocol_id, priority_flag
	_ = readCString(pdu.Body, &offset)
	_ = readCString(pdu.Body, &offset)
	if offset+5 > len(pdu.Body) {
		return message.Message{}, errors.New("missing submit_sm delivery fields")
	}
	offset += 2 // registered_delivery, replace_if_present_flag
	dataCoding := pdu.Body[offset]
	offset++
	offset++ // sm_default_msg_id
	if offset >= len(pdu.Body) {
		return message.Message{}, errors.New("missing sm length")
	}
	smLen := int(pdu.Body[offset])
	offset++
	if offset+smLen > len(pdu.Body) {
		return message.Message{}, errors.New("short short_message")
	}
	text := string(pdu.Body[offset : offset+smLen])
	msg := message.New("", message.DirectionMO, from, to, text)
	if dataCoding == 0x08 {
		msg.Encoding = "ucs2"
	}
	return msg, nil
}
