package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

const validEncryptionKey = "0101010101010101010101010101010101010101010101010101010101010101"

func TestLoadCrypto(t *testing.T) {
	t.Run("defaults to the dev key", func(t *testing.T) {
		setEnv(t, map[string]string{})
		got := config.LoadCrypto()
		if got.EncryptionKey != config.DevEncryptionKey {
			t.Errorf("LoadCrypto().EncryptionKey = %q, want the dev default", got.EncryptionKey)
		}
	})
	t.Run("reads and trims an explicit key", func(t *testing.T) {
		setEnv(t, map[string]string{"ENCRYPTION_KEY": "  " + validEncryptionKey + "  "})
		got := config.LoadCrypto()
		if got.EncryptionKey != validEncryptionKey {
			t.Errorf("LoadCrypto().EncryptionKey = %q, want %q", got.EncryptionKey, validEncryptionKey)
		}
	})
}

func TestCryptoConfigKey(t *testing.T) {
	t.Run("unset key errors", func(t *testing.T) {
		if _, err := (config.CryptoConfig{}).Key(); err == nil {
			t.Fatal("Key() = nil error, want an error")
		}
	})
	t.Run("non-hex key errors", func(t *testing.T) {
		_, err := config.CryptoConfig{EncryptionKey: "not-hex!!"}.Key()
		if err == nil || !contains(err.Error(), "must be hex") {
			t.Errorf("Key() error = %v, want a hex error", err)
		}
	})
	t.Run("wrong-length key errors", func(t *testing.T) {
		_, err := config.CryptoConfig{EncryptionKey: "abcdef"}.Key()
		if err == nil || !contains(err.Error(), "32 bytes") {
			t.Errorf("Key() error = %v, want a 32-bytes error", err)
		}
	})
	t.Run("valid key decodes to 32 bytes", func(t *testing.T) {
		key, err := config.CryptoConfig{EncryptionKey: validEncryptionKey}.Key()
		if err != nil {
			t.Fatalf("Key() unexpected error: %v", err)
		}
		if len(key) != 32 {
			t.Errorf("Key() len = %d, want 32", len(key))
		}
	})
}

func TestCryptoConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.CryptoConfig
		env          string
		wantContains []string
	}{
		{
			name: "empty key is fine outside prod",
			cfg:  config.CryptoConfig{},
			env:  config.EnvDev,
		},
		{
			name:         "non-hex key is rejected in any env",
			cfg:          config.CryptoConfig{EncryptionKey: "not-hex!!"},
			env:          config.EnvDev,
			wantContains: []string{"ENCRYPTION_KEY must be hex"},
		},
		{
			name:         "wrong-length key is rejected",
			cfg:          config.CryptoConfig{EncryptionKey: "abcdef"},
			env:          config.EnvDev,
			wantContains: []string{"ENCRYPTION_KEY must decode to 32 bytes"},
		},
		{
			name: "prod accepts a valid non-default key",
			cfg:  config.CryptoConfig{EncryptionKey: validEncryptionKey},
			env:  config.EnvProd,
		},
		{
			name:         "prod rejects an unset key",
			cfg:          config.CryptoConfig{},
			env:          config.EnvProd,
			wantContains: []string{"ENCRYPTION_KEY is not set"},
		},
		{
			name:         "prod rejects the default key",
			cfg:          config.CryptoConfig{EncryptionKey: config.DevEncryptionKey},
			env:          config.EnvProd,
			wantContains: []string{"ENCRYPTION_KEY must be set to a non-default value in prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.Validate(tt.env)
			if len(tt.wantContains) == 0 {
				if len(errs) > 0 {
					t.Errorf("Validate() = %v, want no errors", errs)
				}
				return
			}
			joined := errsToString(errs)
			for _, want := range tt.wantContains {
				if !contains(joined, want) {
					t.Errorf("Validate() = %q, want it to contain %q", joined, want)
				}
			}
		})
	}
}

// TestCryptoConfigValidateProdMalformedKeyNoDuplicate is a regression test:
// a non-empty malformed key in prod must be reported once, not twice — the
// general "when set, must be valid" check and the prod-only "must be valid"
// check both cover this exact case, so Validate must not call Key() from
// both and append the same error twice.
func TestCryptoConfigValidateProdMalformedKeyNoDuplicate(t *testing.T) {
	errs := config.CryptoConfig{EncryptionKey: "not-hex!!"}.Validate(config.EnvProd)
	if len(errs) != 1 {
		t.Fatalf("Validate() = %v (%d errors), want exactly 1", errs, len(errs))
	}
	if !contains(errs[0].Error(), "must be hex") {
		t.Errorf("Validate()[0] = %q, want a hex error", errs[0].Error())
	}
}
