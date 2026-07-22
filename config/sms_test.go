package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadSMS(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.SMSConfig
	}{
		{name: "disabled by default", env: map[string]string{}, want: config.SMSConfig{RetryMaxAttempts: 3}},
		{
			name: "enabled with static credentials and an explicit retry cap",
			env: map[string]string{
				"NOTIFY_SMS_ENABLED": "true", "SMS_ORIGINATION_IDENTITY": "+18005550100", "SMS_REGION": "us-east-1",
				"SMS_ACCESS_KEY_ID": "AKIAEXAMPLE", "SMS_SECRET_ACCESS_KEY": "secret", "SMS_RETRY_MAX_ATTEMPTS": "5",
			},
			want: config.SMSConfig{
				Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1",
				AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret", RetryMaxAttempts: 5,
			},
		},
		{
			name: "enabled without static credentials uses the default retry cap",
			env: map[string]string{
				"NOTIFY_SMS_ENABLED": "true", "SMS_ORIGINATION_IDENTITY": "+18005550100", "SMS_REGION": "us-east-1",
			},
			want: config.SMSConfig{Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1", RetryMaxAttempts: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadSMS()
			if len(errs) > 0 {
				t.Fatalf("LoadSMS() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadSMS() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadSMSDisabledIgnoresMalformedSettings is a regression test: a
// disabled deployment (the default) must load successfully even with a
// malformed SMS_RETRY_MAX_ATTEMPTS, because that field is parsed only when
// NOTIFY_SMS_ENABLED is actually true.
func TestLoadSMSDisabledIgnoresMalformedSettings(t *testing.T) {
	setEnv(t, map[string]string{"SMS_RETRY_MAX_ATTEMPTS": "not-a-number", "SMS_ACCESS_KEY_ID": "AKIAEXAMPLE"})
	got, errs := config.LoadSMS()
	if len(errs) > 0 {
		t.Fatalf("LoadSMS() unexpected errors: %v", errs)
	}
	want := config.SMSConfig{AccessKeyID: "AKIAEXAMPLE", RetryMaxAttempts: 3}
	if got != want {
		t.Errorf("LoadSMS() = %+v, want %+v", got, want)
	}
}

func TestLoadSMSMalformedEnabledFlag(t *testing.T) {
	setEnv(t, map[string]string{"NOTIFY_SMS_ENABLED": "maybe"})
	_, errs := config.LoadSMS()
	if len(errs) == 0 || !contains(errsToString(errs), "NOTIFY_SMS_ENABLED") {
		t.Errorf("LoadSMS() errors = %v, want a NOTIFY_SMS_ENABLED error", errs)
	}
}

func TestLoadSMSEnabledWithMalformedRetryAttempts(t *testing.T) {
	setEnv(t, map[string]string{
		"NOTIFY_SMS_ENABLED": "true", "SMS_ORIGINATION_IDENTITY": "+18005550100",
		"SMS_REGION": "us-east-1", "SMS_RETRY_MAX_ATTEMPTS": "abc",
	})
	_, errs := config.LoadSMS()
	if len(errs) == 0 || !contains(errsToString(errs), "SMS_RETRY_MAX_ATTEMPTS") {
		t.Errorf("LoadSMS() errors = %v, want a SMS_RETRY_MAX_ATTEMPTS error", errs)
	}
}

func TestSMSConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.SMSConfig
		wantContains []string
	}{
		{
			name: "disabled ignores every other field",
			cfg:  config.SMSConfig{AccessKeyID: "AKIAEXAMPLE"},
		},
		{
			name: "valid enabled config passes",
			cfg:  config.SMSConfig{Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1", RetryMaxAttempts: 3},
		},
		{
			name:         "enabled without origination identity or region",
			cfg:          config.SMSConfig{Enabled: true, RetryMaxAttempts: 3},
			wantContains: []string{"SMS_ORIGINATION_IDENTITY", "SMS_REGION"},
		},
		{
			name:         "enabled with a non-positive retry max attempts",
			cfg:          config.SMSConfig{Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1", RetryMaxAttempts: 0},
			wantContains: []string{"SMS_RETRY_MAX_ATTEMPTS", "positive"},
		},
		{
			name: "access key without secret",
			cfg: config.SMSConfig{
				Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1",
				RetryMaxAttempts: 3, AccessKeyID: "AKIAEXAMPLE",
			},
			wantContains: []string{"SMS_ACCESS_KEY_ID", "SMS_SECRET_ACCESS_KEY"},
		},
		{
			name: "secret without access key",
			cfg: config.SMSConfig{
				Enabled: true, OriginationIdentity: "+18005550100", Region: "us-east-1",
				RetryMaxAttempts: 3, SecretAccessKey: "secret",
			},
			wantContains: []string{"SMS_ACCESS_KEY_ID", "SMS_SECRET_ACCESS_KEY"},
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
