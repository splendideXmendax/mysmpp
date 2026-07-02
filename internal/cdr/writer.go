package cdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

type Writer struct {
	cfg       config.CDRConfig
	in        chan Event
	done      chan struct{}
	closed    atomic.Bool
	dropped   atomic.Uint64
	seq       atomic.Uint64
	closeOnce sync.Once
	closeErr  error
}

func NewWriter(cfg config.CDRConfig) (*Writer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = 10000
	}
	if cfg.Buffer <= 0 {
		cfg.Buffer = 1
	}
	if cfg.OnFull == "" {
		cfg.OnFull = "block"
	}
	if cfg.FsyncInterval == "" {
		cfg.FsyncInterval = "2s"
	}
	if cfg.MaxAge == "" {
		cfg.MaxAge = "1h"
	}
	if cfg.Instance == "" {
		host, _ := os.Hostname()
		cfg.Instance = host
	}
	if cfg.Mode == "" {
		cfg.Mode = "events"
	}
	if cfg.Mode != "events" {
		return nil, fmt.Errorf("cdr mode %q is not implemented", cfg.Mode)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	w := &Writer{
		cfg:  cfg,
		in:   make(chan Event, cfg.Buffer),
		done: make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

func (w *Writer) Emit(e Event) {
	if w == nil || w.closed.Load() {
		return
	}
	e.Seq = w.seq.Add(1)
	e.normalize(w.cfg.Instance, w.cfg.MaskTo, w.cfg.StoreText)
	if w.cfg.OnFull == "drop" {
		select {
		case w.in <- e:
		default:
			w.dropped.Add(1)
		}
		return
	}
	w.in <- e
}

func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.in)
		<-w.done
	})
	return w.closeErr
}

func (w *Writer) loop() {
	defer close(w.done)
	maxAge, _ := time.ParseDuration(w.cfg.MaxAge)
	fsyncInterval, _ := time.ParseDuration(w.cfg.FsyncInterval)
	var current *cdrFile
	var lastSync time.Time
	var ageTimer *time.Timer
	var syncTicker *time.Ticker
	if fsyncInterval > 0 {
		syncTicker = time.NewTicker(fsyncInterval)
		defer syncTicker.Stop()
	}
	resetAge := func() {
		if maxAge <= 0 {
			return
		}
		if ageTimer == nil {
			ageTimer = time.NewTimer(maxAge)
			return
		}
		if !ageTimer.Stop() {
			select {
			case <-ageTimer.C:
			default:
			}
		}
		ageTimer.Reset(maxAge)
	}
	defer func() {
		if ageTimer != nil {
			ageTimer.Stop()
		}
		if current != nil {
			if err := current.close(true); err != nil && w.closeErr == nil {
				w.closeErr = err
			}
		}
	}()
	for {
		var ageC <-chan time.Time
		var syncC <-chan time.Time
		if ageTimer != nil {
			ageC = ageTimer.C
		}
		if syncTicker != nil {
			syncC = syncTicker.C
		}
		select {
		case e, ok := <-w.in:
			if !ok {
				return
			}
			if current == nil {
				f, err := openCDRFile(w.cfg.Dir, w.cfg.Instance)
				if err != nil {
					w.closeErr = err
					continue
				}
				current = f
				resetAge()
				lastSync = time.Now()
			}
			if err := current.write(e); err != nil {
				w.closeErr = err
				continue
			}
			if w.cfg.FsyncEvery > 0 && current.count%w.cfg.FsyncEvery == 0 {
				_ = current.flushSync()
				lastSync = time.Now()
			}
			if current.count >= w.cfg.MaxRecords {
				if err := current.close(true); err != nil && w.closeErr == nil {
					w.closeErr = err
				}
				current = nil
				resetAge()
			}
		case <-ageC:
			if current != nil {
				if err := current.close(true); err != nil && w.closeErr == nil {
					w.closeErr = err
				}
				current = nil
			}
			resetAge()
		case <-syncC:
			if current != nil && time.Since(lastSync) >= fsyncInterval {
				_ = current.flushSync()
				lastSync = time.Now()
			}
		}
	}
}

type cdrFile struct {
	file     *os.File
	writer   *bufio.Writer
	writing  string
	final    string
	count    int
	openTime time.Time
}

func openCDRFile(dir, instance string) (*cdrFile, error) {
	now := time.Now().UTC()
	base := fmt.Sprintf("cdr-%s-%s", now.Format("20060102150405"), safeInstance(instance))
	writing := filepath.Join(dir, base+".jsonl.writing")
	file, err := os.OpenFile(writing, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		writing = filepath.Join(dir, fmt.Sprintf("%s-%d.jsonl.writing", base, now.UnixNano()))
		file, err = os.OpenFile(writing, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, err
	}
	return &cdrFile{file: file, writer: bufio.NewWriterSize(file, 64*1024), writing: writing, openTime: now}, nil
}

func (f *cdrFile) write(e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.writer.Write(data); err != nil {
		return err
	}
	if err := f.writer.WriteByte('\n'); err != nil {
		return err
	}
	f.count++
	return nil
}

func (f *cdrFile) flushSync() error {
	if err := f.writer.Flush(); err != nil {
		return err
	}
	return f.file.Sync()
}

func (f *cdrFile) close(finalize bool) error {
	if f.file == nil {
		return nil
	}
	err := f.flushSync()
	if closeErr := f.file.Close(); err == nil {
		err = closeErr
	}
	f.file = nil
	if !finalize || f.count == 0 {
		if f.count == 0 {
			_ = os.Remove(f.writing)
		}
		return err
	}
	f.final = filepath.Join(filepath.Dir(f.writing), strings.TrimSuffix(filepath.Base(f.writing), ".jsonl.writing")+fmt.Sprintf("-%d.jsonl", f.count))
	if renameErr := os.Rename(f.writing, f.final); err == nil {
		err = renameErr
	}
	return err
}

func safeInstance(value string) string {
	if value == "" {
		return "default"
	}
	out := []rune(value)
	for i, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		out[i] = '_'
	}
	return string(out)
}
