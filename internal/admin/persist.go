package admin

import "github.com/splendideXmendax/mysmpp/internal/config"

func AtomicWrite(path string, cfg config.Config) error {
	return config.AtomicWrite(path, cfg)
}
