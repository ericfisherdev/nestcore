package config_test

import (
	"strings"
	"testing"
)

// allKeys is every environment variable a loader in this package reads.
// Each test sets all of them (defaulting to "") so cases are isolated from
// the developer's ambient environment, not just from each other.
var allKeys = []string{
	"PORT", "APP_ENV", "DATABASE_URL", "DB_MAX_CONNS", "DB_CONNECT_TIMEOUT",
	"DB_PROVIDER", "DB_POOL_MODE", "DB_SSL_ROOT_CERT", "MIGRATE_DATABASE_URL",
	"TRUSTED_PROXIES", "SERVER_REQUEST_TIMEOUT", "PUBLIC_BASE_URL",
	"SESSION_SECRET", "SESSION_LIFETIME", "SESSION_COOKIE_SECURE",
	"ENCRYPTION_KEY",
	"TLS_CERT_FILE", "TLS_KEY_FILE",
	"HSTS_ENABLED", "HSTS_MAX_AGE", "HSTS_INCLUDE_SUBDOMAINS", "HSTS_PRELOAD",
	"S3_ENDPOINT", "S3_REGION", "S3_BUCKET",
	"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "S3_PRESIGN_TTL",
	"NOTIFY_SMS_ENABLED", "SMS_ORIGINATION_IDENTITY", "SMS_REGION",
	"SMS_ACCESS_KEY_ID", "SMS_SECRET_ACCESS_KEY", "SMS_RETRY_MAX_ATTEMPTS",
	"NOTIFY_EMAIL_ENABLED", "SES_FROM_ADDRESS", "SES_REGION",
	"SES_ACCESS_KEY_ID", "SES_SECRET_ACCESS_KEY",
	"CACHE_DIR",
}

// setEnv isolates a test case from both the developer's ambient environment
// and any local .env file. t.Chdir (like t.Setenv) auto-restores and forbids
// t.Parallel.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, k := range allKeys {
		// t.Setenv isolates each case and auto-restores afterwards.
		// Unspecified keys are cleared.
		t.Setenv(k, env[k])
	}
}

// errsToString joins a slice of errors into one string for substring
// assertions, mirroring how errors.Join(errs...).Error() reads.
func errsToString(errs []error) string {
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "\n")
}

// contains reports whether s contains substr; a thin wrapper so callers read
// as a plain assertion helper alongside errsToString.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
