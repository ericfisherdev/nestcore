package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

var validSessionSecret = strings.Repeat("a", 32)

func TestLoadSession(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		appEnv string
		want   config.SessionConfig
	}{
		{
			name:   "defaults when empty",
			env:    map[string]string{},
			appEnv: config.EnvDev,
			want:   config.SessionConfig{Secret: config.DevSessionSecret, Secure: false, Lifetime: 12 * time.Hour},
		},
		{
			name:   "parsed lifetime override",
			env:    map[string]string{"SESSION_LIFETIME": "48h"},
			appEnv: config.EnvDev,
			want:   config.SessionConfig{Secret: config.DevSessionSecret, Secure: false, Lifetime: 48 * time.Hour},
		},
		{
			name:   "auto in dev stays insecure",
			env:    map[string]string{"SESSION_COOKIE_SECURE": "auto"},
			appEnv: config.EnvDev,
			want:   config.SessionConfig{Secret: config.DevSessionSecret, Secure: false, Lifetime: 12 * time.Hour},
		},
		{
			name:   "auto in prod is secure",
			env:    map[string]string{"SESSION_COOKIE_SECURE": "auto", "SESSION_SECRET": validSessionSecret},
			appEnv: config.EnvProd,
			want:   config.SessionConfig{Secret: validSessionSecret, Secure: true, Lifetime: 12 * time.Hour},
		},
		{
			name:   "true forces secure outside prod",
			env:    map[string]string{"SESSION_COOKIE_SECURE": "true"},
			appEnv: config.EnvDev,
			want:   config.SessionConfig{Secret: config.DevSessionSecret, Secure: true, Lifetime: 12 * time.Hour},
		},
		{
			name:   "false overrides prod auto",
			env:    map[string]string{"SESSION_COOKIE_SECURE": "false", "SESSION_SECRET": validSessionSecret},
			appEnv: config.EnvProd,
			want:   config.SessionConfig{Secret: validSessionSecret, Secure: false, Lifetime: 12 * time.Hour},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadSession(tt.appEnv)
			if len(errs) > 0 {
				t.Fatalf("LoadSession() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadSession() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadSessionInvalidCookieMode(t *testing.T) {
	setEnv(t, map[string]string{"SESSION_COOKIE_SECURE": "maybe"})
	_, errs := config.LoadSession(config.EnvDev)
	joined := errsToString(errs)
	if !contains(joined, "SESSION_COOKIE_SECURE") {
		t.Errorf("LoadSession() errors = %q, want it to name SESSION_COOKIE_SECURE", joined)
	}
}

func TestLoadSessionInvalidLifetime(t *testing.T) {
	setEnv(t, map[string]string{"SESSION_LIFETIME": "5x"})
	_, errs := config.LoadSession(config.EnvDev)
	if len(errs) == 0 || !contains(errsToString(errs), "SESSION_LIFETIME") {
		t.Errorf("LoadSession() errors = %v, want a SESSION_LIFETIME error", errs)
	}
}

func TestSessionConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.SessionConfig
		env          string
		wantContains []string
	}{
		{
			name: "valid config passes",
			cfg:  config.SessionConfig{Secret: validSessionSecret, Lifetime: time.Hour},
			env:  config.EnvDev,
		},
		{
			name:         "short secret",
			cfg:          config.SessionConfig{Secret: "too-short", Lifetime: time.Hour},
			env:          config.EnvDev,
			wantContains: []string{"SESSION_SECRET", "32"},
		},
		{
			name:         "non-positive lifetime",
			cfg:          config.SessionConfig{Secret: validSessionSecret, Lifetime: -5 * time.Minute},
			env:          config.EnvDev,
			wantContains: []string{"SESSION_LIFETIME", "positive"},
		},
		{
			name:         "prod rejects the default secret",
			cfg:          config.SessionConfig{Secret: config.DevSessionSecret, Lifetime: time.Hour},
			env:          config.EnvProd,
			wantContains: []string{"non-default"},
		},
		{
			name: "dev allows the default secret",
			cfg:  config.SessionConfig{Secret: config.DevSessionSecret, Lifetime: time.Hour},
			env:  config.EnvDev,
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
