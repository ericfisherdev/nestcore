package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadEmail(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.EmailConfig
	}{
		{name: "disabled by default", env: map[string]string{}, want: config.EmailConfig{}},
		{
			name: "enabled with static credentials",
			env: map[string]string{
				"NOTIFY_EMAIL_ENABLED": "true", "SES_FROM_ADDRESS": "notify@example.com", "SES_REGION": "us-east-1",
				"SES_ACCESS_KEY_ID": "AKIAEXAMPLE", "SES_SECRET_ACCESS_KEY": "secret",
			},
			want: config.EmailConfig{
				Enabled: true, FromAddress: "notify@example.com", Region: "us-east-1",
				AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret",
			},
		},
		{
			name: "enabled without static credentials",
			env:  map[string]string{"NOTIFY_EMAIL_ENABLED": "true", "SES_FROM_ADDRESS": "notify@example.com", "SES_REGION": "us-east-1"},
			want: config.EmailConfig{Enabled: true, FromAddress: "notify@example.com", Region: "us-east-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadEmail()
			if len(errs) > 0 {
				t.Fatalf("LoadEmail() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadEmail() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadEmailMalformedEnabledFlag(t *testing.T) {
	setEnv(t, map[string]string{"NOTIFY_EMAIL_ENABLED": "maybe"})
	_, errs := config.LoadEmail()
	if len(errs) == 0 || !contains(errsToString(errs), "NOTIFY_EMAIL_ENABLED") {
		t.Errorf("LoadEmail() errors = %v, want a NOTIFY_EMAIL_ENABLED error", errs)
	}
}

// TestLoadEmailDisabledIgnoresPartialSettings is a regression test: a
// disabled deployment (the default) must load successfully even with a lone
// SES_ACCESS_KEY_ID, since the credential pairing check only runs when
// NOTIFY_EMAIL_ENABLED is actually true.
func TestLoadEmailDisabledIgnoresPartialSettings(t *testing.T) {
	setEnv(t, map[string]string{"SES_ACCESS_KEY_ID": "AKIAEXAMPLE"})
	got, errs := config.LoadEmail()
	if len(errs) > 0 {
		t.Fatalf("LoadEmail() unexpected errors: %v", errs)
	}
	if got.Validate() != nil {
		t.Errorf("Validate() = %v, want no errors while disabled", got.Validate())
	}
}

func TestEmailConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.EmailConfig
		wantContains []string
	}{
		{name: "disabled ignores every other field", cfg: config.EmailConfig{AccessKeyID: "AKIAEXAMPLE"}},
		{name: "valid enabled config passes", cfg: config.EmailConfig{Enabled: true, FromAddress: "notify@example.com", Region: "us-east-1"}},
		{
			name:         "enabled without from address or region",
			cfg:          config.EmailConfig{Enabled: true},
			wantContains: []string{"SES_FROM_ADDRESS", "SES_REGION"},
		},
		{
			name:         "access key without secret",
			cfg:          config.EmailConfig{Enabled: true, FromAddress: "notify@example.com", Region: "us-east-1", AccessKeyID: "AKIAEXAMPLE"},
			wantContains: []string{"SES_ACCESS_KEY_ID", "SES_SECRET_ACCESS_KEY"},
		},
		{
			name:         "secret without access key",
			cfg:          config.EmailConfig{Enabled: true, FromAddress: "notify@example.com", Region: "us-east-1", SecretAccessKey: "secret"},
			wantContains: []string{"SES_ACCESS_KEY_ID", "SES_SECRET_ACCESS_KEY"},
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
