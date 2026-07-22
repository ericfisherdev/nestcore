package config

import (
	"errors"
	"fmt"
)

// defaultSMSRetryMaxAttempts is SMS_RETRY_MAX_ATTEMPTS's default: kept
// tight deliberately, since SMS is typically billed per attempt handed to
// the carrier, so a generous retry budget is a real spend risk against a
// persistently failing destination, not just a latency one.
const defaultSMSRetryMaxAttempts = 3

// SMSConfig configures an optional SMS notification channel. It is only
// consulted when Enabled is true; every other field is otherwise ignored
// (and unvalidated) — a deployment with SMS disabled (the default) never
// has to set any of it.
type SMSConfig struct {
	// Enabled turns the SMS channel on.
	Enabled bool
	// OriginationIdentity is the verified sending number (or its ARN, or a
	// pool id/ARN) messages are sent from. Required when Enabled.
	OriginationIdentity string
	// Region is passed to every SMS API request. Required when Enabled.
	Region string
	// AccessKeyID / SecretAccessKey are optional static credentials. When
	// BOTH are blank, the AWS SDK's default credential chain supplies
	// credentials instead — mirrors S3Config's identical field pair.
	AccessKeyID     string
	SecretAccessKey string
	// RetryMaxAttempts caps the AWS SDK's own built-in retryer.
	RetryMaxAttempts int
}

// LoadSMS reads SMSConfig from NOTIFY_SMS_ENABLED, SMS_ORIGINATION_IDENTITY,
// SMS_REGION, SMS_ACCESS_KEY_ID, SMS_SECRET_ACCESS_KEY, and
// SMS_RETRY_MAX_ATTEMPTS. NOTIFY_SMS_ENABLED gates every other SMS_*
// setting: SMS_RETRY_MAX_ATTEMPTS is parsed (and its parse error collected)
// only when SMS is enabled, so a deployment with SMS disabled (the default)
// never fails to load on a stray or malformed value it will never use.
func LoadSMS() (SMSConfig, []error) {
	var errs []error

	enabled, err := Bool("NOTIFY_SMS_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}

	retryMaxAttempts := defaultSMSRetryMaxAttempts
	if enabled {
		attempts, err := Int32("SMS_RETRY_MAX_ATTEMPTS", defaultSMSRetryMaxAttempts)
		if err != nil {
			errs = append(errs, err)
		}
		retryMaxAttempts = int(attempts)
	}

	return SMSConfig{
		Enabled:             enabled,
		OriginationIdentity: trimmed("SMS_ORIGINATION_IDENTITY"),
		Region:              trimmed("SMS_REGION"),
		AccessKeyID:         trimmed("SMS_ACCESS_KEY_ID"),
		SecretAccessKey:     trimmed("SMS_SECRET_ACCESS_KEY"),
		RetryMaxAttempts:    retryMaxAttempts,
	}, errs
}

// Validate returns every SMSConfig problem found, so callers can surface
// them together. Every check below runs only when Enabled is true, so a
// deployment with SMS disabled never fails validation on a stray or partial
// SMS_* value it will never use.
func (s SMSConfig) Validate() []error {
	if !s.Enabled {
		return nil
	}

	var errs []error
	if s.OriginationIdentity == "" {
		errs = append(errs, errors.New("SMS_ORIGINATION_IDENTITY is required when NOTIFY_SMS_ENABLED=true"))
	}
	if s.Region == "" {
		errs = append(errs, errors.New("SMS_REGION is required when NOTIFY_SMS_ENABLED=true"))
	}
	if s.RetryMaxAttempts <= 0 {
		errs = append(errs, fmt.Errorf("SMS_RETRY_MAX_ATTEMPTS must be positive, got %d", s.RetryMaxAttempts))
	}
	if err := validateCredentialPair("SMS_ACCESS_KEY_ID", s.AccessKeyID, "SMS_SECRET_ACCESS_KEY", s.SecretAccessKey); err != nil {
		errs = append(errs, err)
	}

	return errs
}
