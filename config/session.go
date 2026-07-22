package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	// minSecretLen is the minimum acceptable SESSION_SECRET length in
	// bytes.
	minSecretLen = 32

	// SESSION_COOKIE_SECURE accepted values. auto is the default and
	// resolves to Secure only when the deployment environment is
	// EnvProd; true/false force it.
	cookieSecureAuto  = "auto"
	cookieSecureTrue  = "true"
	cookieSecureFalse = "false"
)

// DevSessionSecret is a known, insecure default used only in development.
// It satisfies the length check in dev but is rejected in prod (see
// Validate), forcing a real secret in production.
const DevSessionSecret = "dev-only-insecure-session-secret-change-me"

// SessionConfig configures sessions.
type SessionConfig struct {
	// Secret is a high-entropy key reserved for cryptographic signing; it
	// must be at least minSecretLen bytes.
	Secret string
	// Secure marks the session cookie Secure (HTTPS-only). It is resolved
	// from SESSION_COOKIE_SECURE: auto (the default) keeps Secure only when
	// the deployment environment is EnvProd, while true/false force it —
	// letting a TLS-terminated deployment emit Secure cookies regardless of
	// environment.
	Secure bool
	// Lifetime is the maximum session duration.
	Lifetime time.Duration
}

// LoadSession reads SessionConfig from SESSION_SECRET, SESSION_LIFETIME, and
// SESSION_COOKIE_SECURE. env resolves SESSION_COOKIE_SECURE's auto setting
// against the deployment environment (EnvDev, EnvTest, or EnvProd).
func LoadSession(env string) (SessionConfig, []error) {
	var errs []error

	lifetime, err := Duration("SESSION_LIFETIME", 12*time.Hour)
	if err != nil {
		errs = append(errs, err)
	}
	secure, err := resolveCookieSecure(String("SESSION_COOKIE_SECURE", cookieSecureAuto), env)
	if err != nil {
		errs = append(errs, err)
	}

	return SessionConfig{
		Secret:   String("SESSION_SECRET", DevSessionSecret),
		Secure:   secure,
		Lifetime: lifetime,
	}, errs
}

// Validate returns every SessionConfig problem found, so callers can
// surface them together. env additionally gates the prod-only rejection of
// the development default secret.
func (s SessionConfig) Validate(env string) []error {
	var errs []error

	if len(s.Secret) < minSecretLen {
		errs = append(errs, fmt.Errorf("SESSION_SECRET must be at least %d bytes, got %d", minSecretLen, len(s.Secret)))
	}
	if s.Lifetime <= 0 {
		errs = append(errs, fmt.Errorf("SESSION_LIFETIME must be positive, got %v", s.Lifetime))
	}
	if env == EnvProd && s.Secret == DevSessionSecret {
		errs = append(errs, fmt.Errorf("SESSION_SECRET must be set to a non-default value in %s", EnvProd))
	}

	return errs
}

// resolveCookieSecure maps SESSION_COOKIE_SECURE to the session cookie's
// Secure flag. auto (the default) resolves to Secure only when env is
// EnvProd; true/false force it independently of env. An unknown value falls
// back to the prod-derived default and is reported.
func resolveCookieSecure(mode, env string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case cookieSecureAuto, "":
		return env == EnvProd, nil
	case cookieSecureTrue:
		return true, nil
	case cookieSecureFalse:
		return false, nil
	default:
		return env == EnvProd, fmt.Errorf("SESSION_COOKIE_SECURE must be one of %s|%s|%s, got %q",
			cookieSecureAuto, cookieSecureTrue, cookieSecureFalse, mode)
	}
}
