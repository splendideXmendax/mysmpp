package cdr

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func TestWriterRotatesByMaxRecordsAndClosesWritingFile(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(config.CDRConfig{
		Enabled:       true,
		Dir:           dir,
		Mode:          "events",
		MaxRecords:    2,
		MaxAge:        "1h",
		Buffer:        8,
		OnFull:        "block",
		FsyncInterval: "0s",
		Instance:      "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Emit(Event{Kind: "accepted", GatewayID: "g1"})
	w.Emit(Event{Kind: "sent", GatewayID: "g1"})
	w.Emit(Event{Kind: "dlr", GatewayID: "g1"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected two finalized files, got %v", files)
	}
	writing, _ := filepath.Glob(filepath.Join(dir, "*.writing"))
	if len(writing) != 0 {
		t.Fatalf("writing files should be finalized: %v", writing)
	}
	total := 0
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if !strings.Contains(scanner.Text(), `"kind"`) {
				t.Fatalf("line is not json event: %s", scanner.Text())
			}
			total++
		}
		_ = f.Close()
	}
	if total != 3 {
		t.Fatalf("expected 3 cdr lines, got %d", total)
	}
}

func TestWriterDoesNotOverwriteSameSecondRotations(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(config.CDRConfig{
		Enabled:       true,
		Dir:           dir,
		Mode:          "events",
		MaxRecords:    3,
		MaxAge:        "1h",
		Buffer:        16,
		OnFull:        "block",
		FsyncEvery:    1,
		FsyncInterval: "0s",
		Instance:      "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		w.Emit(Event{Kind: "accepted", GatewayID: "g"})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected two finalized files without overwrite, got %v", files)
	}
	total := 0
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		lines := 0
		for scanner.Scan() {
			lines++
			total++
		}
		_ = f.Close()
		if lines != 3 {
			t.Fatalf("expected %s to have 3 lines, got %d", file, lines)
		}
	}
	if total != 6 {
		t.Fatalf("expected 6 lines across rotated files, got %d", total)
	}
}
