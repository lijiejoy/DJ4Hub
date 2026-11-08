package sip

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config mirrors the xiaoguiday/vohive SIP shape:
// sip.listen (UDP), realm, RTP range, one configured local user.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Listen   string `json:"listen"`   // e.g. "127.0.0.1:5060"
	Realm    string `json:"realm"`    // e.g. "vohive.local"
	Username string `json:"username"` // e.g. "linphone"
	Password string `json:"-"`        // never serialised; lives in its own 0600 file
	RTPMin   int    `json:"rtp_min"`  // e.g. 10000
	RTPMax   int    `json:"rtp_max"`  // e.g. 20000
}

// DefaultConfig returns the shipped defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:  true,
		Listen:   "127.0.0.1:5060",
		Realm:    "vohive.local",
		Username: "linphone",
		RTPMin:   10000,
		RTPMax:   20000,
	}
}

// Validate checks the config for usable values.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("sip listen address is empty")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("invalid sip listen address %q: %w", c.Listen, err)
	}
	if host == "" {
		return errors.New("sip listen address must bind a host (loopback-first: 127.0.0.1)")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid sip listen port %q", port)
	}
	if strings.TrimSpace(c.Realm) == "" {
		return errors.New("sip realm is empty")
	}
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("sip username is empty")
	}
	if c.RTPMin < 1024 || c.RTPMax <= c.RTPMin || c.RTPMax > 65535 {
		return fmt.Errorf("invalid RTP range %d-%d", c.RTPMin, c.RTPMax)
	}
	return nil
}

const (
	sipConfigFileName   = "sip-config.json"
	sipPasswordFileName = "sip-password"
)

const passwordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GeneratePassword returns a random 20-character password. It is only ever
// produced on first run (or when the operator explicitly rotates it); no
// password is ever hardcoded.
func GeneratePassword() (string, error) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate SIP password: %w", err)
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// LoadOrCreate loads sip-config.json + sip-password from dir (created with
// 0700 when missing). When no password exists, one is generated, stored with
// mode 0600, and generated=true is returned so the caller can print it once
// (first-run pattern). The password is never written into the JSON file.
func LoadOrCreate(dir string) (cfg Config, generated bool, err error) {
	cfg = DefaultConfig()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cfg, false, fmt.Errorf("create SIP config dir: %w", err)
	}
	configPath := filepath.Join(dir, sipConfigFileName)
	configExisted := false
	if raw, readErr := os.ReadFile(configPath); readErr == nil {
		configExisted = true
		var stored Config
		if jsonErr := json.Unmarshal(raw, &stored); jsonErr == nil {
			if stored.Listen != "" {
				cfg.Listen = stored.Listen
			}
			if stored.Realm != "" {
				cfg.Realm = stored.Realm
			}
			if stored.Username != "" {
				cfg.Username = stored.Username
			}
			if stored.RTPMin != 0 {
				cfg.RTPMin = stored.RTPMin
			}
			if stored.RTPMax != 0 {
				cfg.RTPMax = stored.RTPMax
			}
			cfg.Enabled = stored.Enabled
		}
	}
	passwordPath := filepath.Join(dir, sipPasswordFileName)
	if raw, readErr := os.ReadFile(passwordPath); readErr == nil {
		cfg.Password = strings.TrimSpace(string(raw))
	}
	if cfg.Password == "" {
		password, genErr := GeneratePassword()
		if genErr != nil {
			return cfg, false, genErr
		}
		if writeErr := os.WriteFile(passwordPath, []byte(password+"\n"), 0o600); writeErr != nil {
			return cfg, false, fmt.Errorf("store SIP password: %w", writeErr)
		}
		cfg.Password = password
		generated = true
	}
	if err := cfg.Validate(); err != nil {
		return cfg, generated, err
	}
	if !configExisted {
		// Persist the (possibly defaulted) config so later runs and the
		// settings UI see the same values.
		if saveErr := cfg.Save(dir); saveErr != nil {
			return cfg, generated, saveErr
		}
	}
	return cfg, generated, nil
}

// Save persists the non-secret part of cfg to dir/sip-config.json. The
// password is handled separately by SavePassword and is never written here.
func (c Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create SIP config dir: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, sipConfigFileName+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, sipConfigFileName))
}

// SavePassword stores password with mode 0600.
func SavePassword(dir, password string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sipPasswordFileName), []byte(strings.TrimSpace(password)+"\n"), 0o600)
}
