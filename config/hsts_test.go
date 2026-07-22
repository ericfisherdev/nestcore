package config_test

import (
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadHSTS(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.HSTSConfig
	}{
		{name: "defaults when empty", env: map[string]string{}, want: config.HSTSConfig{}},
		{
			name: "parameters are captured",
			env: map[string]string{
				"HSTS_ENABLED": "true", "HSTS_MAX_AGE": "8760h",
				"HSTS_INCLUDE_SUBDOMAINS": "true", "HSTS_PRELOAD": "true",
			},
			want: config.HSTSConfig{Enabled: true, MaxAge: 8760 * time.Hour, MaxAgeSet: true, IncludeSubdomains: true, Preload: true},
		},
		{
			// An explicit max-age=0 is captured as SET (not "unset"), so the
			// consumer can emit max-age=0 to clear a previously-sent HSTS
			// policy.
			name: "explicit max-age=0 is captured as set",
			env:  map[string]string{"HSTS_ENABLED": "true", "HSTS_MAX_AGE": "0s"},
			want: config.HSTSConfig{Enabled: true, MaxAge: 0, MaxAgeSet: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadHSTS()
			if len(errs) > 0 {
				t.Fatalf("LoadHSTS() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadHSTS() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadHSTSInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "non-boolean HSTS_ENABLED", env: map[string]string{"HSTS_ENABLED": "maybe"}, want: "HSTS_ENABLED"},
		{name: "non-duration HSTS_MAX_AGE", env: map[string]string{"HSTS_MAX_AGE": "5x"}, want: "HSTS_MAX_AGE"},
		{name: "non-boolean HSTS_INCLUDE_SUBDOMAINS", env: map[string]string{"HSTS_INCLUDE_SUBDOMAINS": "maybe"}, want: "HSTS_INCLUDE_SUBDOMAINS"},
		{name: "non-boolean HSTS_PRELOAD", env: map[string]string{"HSTS_PRELOAD": "maybe"}, want: "HSTS_PRELOAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			_, errs := config.LoadHSTS()
			if len(errs) == 0 || !contains(errsToString(errs), tt.want) {
				t.Errorf("LoadHSTS() errors = %v, want it to contain %q", errs, tt.want)
			}
		})
	}
}

func TestHSTSConfigEffectiveMaxAge(t *testing.T) {
	t.Run("unset uses the default", func(t *testing.T) {
		h := config.HSTSConfig{}
		if got := h.EffectiveMaxAge(); got != config.DefaultHSTSMaxAge {
			t.Errorf("EffectiveMaxAge() = %v, want %v", got, config.DefaultHSTSMaxAge)
		}
	})
	t.Run("explicit zero is honored, not defaulted", func(t *testing.T) {
		h := config.HSTSConfig{MaxAgeSet: true, MaxAge: 0}
		if got := h.EffectiveMaxAge(); got != 0 {
			t.Errorf("EffectiveMaxAge() = %v, want 0", got)
		}
	})
}

func TestHSTSConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.HSTSConfig
		wantContains []string
	}{
		{name: "disabled passes regardless of fields", cfg: config.HSTSConfig{MaxAge: -time.Hour, MaxAgeSet: true}},
		{
			name:         "negative max-age when enabled",
			cfg:          config.HSTSConfig{Enabled: true, MaxAge: -time.Hour, MaxAgeSet: true},
			wantContains: []string{"HSTS_MAX_AGE"},
		},
		{
			name:         "preload without includeSubDomains",
			cfg:          config.HSTSConfig{Enabled: true, Preload: true, MaxAge: 8760 * time.Hour, MaxAgeSet: true},
			wantContains: []string{"HSTS_PRELOAD", "HSTS_INCLUDE_SUBDOMAINS"},
		},
		{
			name:         "preload with too-short max-age",
			cfg:          config.HSTSConfig{Enabled: true, Preload: true, IncludeSubdomains: true, MaxAge: 168 * time.Hour, MaxAgeSet: true},
			wantContains: []string{"HSTS_PRELOAD", "1 year"},
		},
		{
			name:         "preload with the built-in default max-age still fails (< 1y)",
			cfg:          config.HSTSConfig{Enabled: true, Preload: true, IncludeSubdomains: true},
			wantContains: []string{"HSTS_PRELOAD", "1 year"},
		},
		{
			name: "valid preload config passes",
			cfg:  config.HSTSConfig{Enabled: true, Preload: true, IncludeSubdomains: true, MaxAge: 8760 * time.Hour, MaxAgeSet: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.Validate()
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
