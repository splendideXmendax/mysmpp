package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BootstrapResult struct {
	ConfigPath      string
	CredentialsPath string
	Seeded          bool
	Generated       bool
}

func LoadStartup(path, seedPath string) (Config, BootstrapResult, error) {
	var result BootstrapResult
	result.ConfigPath = path
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) || seedPath == "" {
				return Config{}, result, fmt.Errorf("read config: %w", err)
			}
			if err := copySeedConfig(seedPath, path); err != nil {
				return Config{}, result, fmt.Errorf("seed config: %w", err)
			}
			result.Seeded = true
		}
	}
	cfg, err := Load(path)
	if err != nil {
		return cfg, result, err
	}
	generated := generateStartupSecrets(&cfg)
	if len(generated) == 0 {
		return cfg, result, nil
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, result, fmt.Errorf("validate generated config: %w", err)
	}
	if err := AtomicWrite(path, cfg); err != nil {
		return cfg, result, fmt.Errorf("write generated config: %w", err)
	}
	credPath := filepath.Join(filepath.Dir(path), "credentials.txt")
	if err := writeCredentials(credPath, generated); err != nil {
		return cfg, result, fmt.Errorf("write generated credentials: %w", err)
	}
	result.Generated = true
	result.CredentialsPath = credPath
	return cfg, result, nil
}

func generateStartupSecrets(cfg *Config) map[string]string {
	out := map[string]string{}
	if IsAutoGenerate(cfg.Admin.Password) {
		cfg.Admin.Password = randomSecret()
		out["admin.username"] = cfg.Admin.Username
		out["admin.password"] = cfg.Admin.Password
	}
	if IsAutoGenerate(cfg.SMPP.Password) {
		cfg.SMPP.Password = randomSMPPSecret()
		out["smpp.system_id"] = cfg.SMPP.SystemID
		out["smpp.password"] = cfg.SMPP.Password
	}
	for i := range cfg.ESMEs {
		if IsAutoGenerate(cfg.ESMEs[i].Password) {
			cfg.ESMEs[i].Password = randomSMPPSecret()
			key := fmt.Sprintf("esmes.%s.password", cfg.ESMEs[i].SystemID)
			out[key] = cfg.ESMEs[i].Password
		}
	}
	for i := range cfg.Clients {
		if IsAutoGenerate(cfg.Clients[i].Token) {
			cfg.Clients[i].Token = randomSecret()
			key := fmt.Sprintf("clients.%s.token", cfg.Clients[i].ClientID)
			out[key] = cfg.Clients[i].Token
		}
	}
	for i := range cfg.Inbound {
		if IsAutoGenerate(cfg.Inbound[i].AuthToken) {
			cfg.Inbound[i].AuthToken = randomSecret()
			key := fmt.Sprintf("inbound.%s.auth_token", cfg.Inbound[i].Name)
			out[key] = cfg.Inbound[i].AuthToken
		}
	}
	for i := range cfg.Providers {
		if IsAutoGenerate(cfg.Providers[i].Password) {
			cfg.Providers[i].Password = randomSecret()
			key := fmt.Sprintf("providers.%s.password", cfg.Providers[i].Name)
			out[key] = cfg.Providers[i].Password
		}
	}
	return out
}

func randomSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mysmpp-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func randomSMPPSecret() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano())[:SMPPMaxPassword]
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func copySeedConfig(seedPath, path string) error {
	data, err := os.ReadFile(seedPath)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func writeCredentials(path string, values map[string]string) error {
	var b strings.Builder
	b.WriteString("# mysmpp generated credentials\n")
	b.WriteString("# Generated once on first startup. Keep this file private.\n")
	b.WriteString("generated_at=" + time.Now().UTC().Format(time.RFC3339) + "\n")
	for k, v := range values {
		b.WriteString(k + "=" + v + "\n")
	}
	return AtomicWriteFile(path, []byte(b.String()), 0o600)
}

func AtomicWrite(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(path, data, 0o600)
}

func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path + ".bak")
		if err := os.Rename(path, path+".bak"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}
