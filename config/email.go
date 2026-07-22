package config

import "errors"

// EmailConfig configures an optional email notification channel. It is only
// consulted when Enabled is true; every other field is otherwise ignored
// (and unvalidated), mirroring SMSConfig's identical
// enabled-gates-required-fields pattern — a deployment with email disabled
// (the default) never has to set any of it.
type EmailConfig struct {
	// Enabled turns the email channel on.
	Enabled bool
	// FromAddress is the verified sending address messages are sent from.
	// Required when Enabled.
	FromAddress string
	// Region is passed to every email API request. Required when Enabled.
	Region string
	// AccessKeyID / SecretAccessKey are optional static credentials. When
	// BOTH are blank, the AWS SDK's default credential chain supplies
	// credentials instead — mirrors SMSConfig's identical field pair.
	AccessKeyID     string
	SecretAccessKey string
}

// LoadEmail reads EmailConfig from NOTIFY_EMAIL_ENABLED, SES_FROM_ADDRESS,
// SES_REGION, SES_ACCESS_KEY_ID, and SES_SECRET_ACCESS_KEY.
func LoadEmail() (EmailConfig, []error) {
	var errs []error

	enabled, err := Bool("NOTIFY_EMAIL_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}

	return EmailConfig{
		Enabled:         enabled,
		FromAddress:     trimmed("SES_FROM_ADDRESS"),
		Region:          trimmed("SES_REGION"),
		AccessKeyID:     trimmed("SES_ACCESS_KEY_ID"),
		SecretAccessKey: trimmed("SES_SECRET_ACCESS_KEY"),
	}, errs
}

// Validate returns every EmailConfig problem found, so callers can surface
// them together. Every check below runs only when Enabled is true, so a
// deployment with email disabled never fails validation on a stray or
// partial SES_* value it will never use.
func (e EmailConfig) Validate() []error {
	if !e.Enabled {
		return nil
	}

	var errs []error
	if e.FromAddress == "" {
		errs = append(errs, errors.New("SES_FROM_ADDRESS is required when NOTIFY_EMAIL_ENABLED=true"))
	}
	if e.Region == "" {
		errs = append(errs, errors.New("SES_REGION is required when NOTIFY_EMAIL_ENABLED=true"))
	}
	// Static credentials are both-or-neither, mirroring SMS's identical
	// pairing check.
	if (e.AccessKeyID == "") != (e.SecretAccessKey == "") {
		errs = append(errs, errors.New("SES_ACCESS_KEY_ID and SES_SECRET_ACCESS_KEY must be set together (or both left unset to use the default AWS credential chain)"))
	}

	return errs
}
