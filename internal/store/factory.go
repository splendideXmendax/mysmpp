package store

import (
	"fmt"
	"strings"

	"github.com/splendideXmendax/mysmpp/internal/config"
)

func NewFromConfig(cfg config.Config) (Store, error) {
	switch strings.ToLower(cfg.Storage.Driver) {
	case "", "memory":
		return NewMemory(), nil
	case "file", "json":
		return NewFile(cfg.Storage.DSN)
	default:
		return nil, fmt.Errorf("unsupported storage.driver %q", cfg.Storage.Driver)
	}
}
