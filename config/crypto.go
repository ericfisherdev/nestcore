package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// encryptionKeyLen is the required decoded key length (AES-256).
const encryptionKeyLen = 32

// DevEncryptionKey is a known, insecure 32-byte (64-hex) default used only
// in development so a caller starts without configuration. It is rejected
// in prod (see Validate), forcing a real key (generated with
// `openssl rand -hex 32`) there.
const DevEncryptionKey = "00000000000000000000000000000000000000000000000000000000deadbeef"

// CryptoConfig holds an at-rest encryption key for protecting stored
// secrets. EncryptionKey is a 64-character hex string (32 bytes), produced
// by `openssl rand -hex 32`. When set in any environment it must be valid;
// Validate additionally requires it in prod. Key decodes and validates it.
type CryptoConfig struct {
	EncryptionKey string
}

// Key decodes the configured hex EncryptionKey into its 32 raw bytes,
// returning an error when it is unset or not exactly 32 bytes of hex.
func (c CryptoConfig) Key() ([]byte, error) {
	if c.EncryptionKey == "" {
		return nil, errors.New("ENCRYPTION_KEY is not set")
	}
	key, err := hex.DecodeString(c.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be hex: %w", err)
	}
	if len(key) != encryptionKeyLen {
		return nil, fmt.Errorf("ENCRYPTION_KEY must decode to %d bytes, got %d", encryptionKeyLen, len(key))
	}
	return key, nil
}

// LoadCrypto reads CryptoConfig from ENCRYPTION_KEY, falling back to
// DevEncryptionKey when unset.
func LoadCrypto() CryptoConfig {
	return CryptoConfig{EncryptionKey: strings.TrimSpace(String("ENCRYPTION_KEY", DevEncryptionKey))}
}

// Validate returns every CryptoConfig problem found, so callers can surface
// them together. env additionally gates the prod-only requirement that a
// key be set and non-default: a malformed or default key must fail fast at
// startup rather than at the first encrypt.
func (c CryptoConfig) Validate(env string) []error {
	var errs []error

	// A key must be valid whenever one is provided, and unconditionally in
	// prod — so an unset, whitespace-only, or otherwise malformed key fails
	// fast at startup, not at the first encrypt. A single call covers both:
	// calling Key() a second time inside the prod branch would only
	// duplicate the same error.
	if c.EncryptionKey != "" || env == EnvProd {
		if _, err := c.Key(); err != nil {
			errs = append(errs, err)
		}
	}

	if env == EnvProd && c.EncryptionKey == DevEncryptionKey {
		errs = append(errs, fmt.Errorf("ENCRYPTION_KEY must be set to a non-default value in %s", EnvProd))
	}

	return errs
}
