// Package config loads and validates runtime configuration from the
// environment, one sub-config at a time. Configuration is read exclusively
// from environment variables so secrets are never committed; an optional
// .env file is honored in development only, via LoadDotenv.
//
// Each sub-config has its own loader and its own validator: LoadServer,
// LoadDB, LoadSession, and so on, each paired with a Validate method (or a
// free function taking the sub-config, for the two that also need the
// deployment environment). Every loader that can fail returns the parsed
// value alongside an aggregated slice of errors rather than stopping at the
// first problem, and every fallback is still applied even when a value fails
// to parse, so the caller always has something usable to work with while it
// reports every problem found in one pass.
//
// This package holds only sub-configs shared across more than one
// application. An application's own root configuration struct — and any
// config that is specific to its own domain — lives in that application's
// own module and is composed from the loaders here.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Deployment environments. A caller's own AppEnv value is expected to be one
// of these three.
const (
	EnvDev  = "dev"
	EnvTest = "test"
	EnvProd = "prod"
)

// AppEnv returns the deployment environment from APP_ENV, defaulting to
// EnvDev.
func AppEnv() string {
	return String("APP_ENV", EnvDev)
}

// ValidateAppEnv reports whether env is one of EnvDev, EnvTest, or EnvProd.
func ValidateAppEnv(env string) []error {
	switch env {
	case EnvDev, EnvTest, EnvProd:
		return nil
	default:
		return []error{fmt.Errorf("APP_ENV must be one of %s|%s|%s, got %q", EnvDev, EnvTest, EnvProd, env)}
	}
}

// LoadDotenv loads an optional .env file from the current working directory
// into the process environment. godotenv.Load never overwrites a variable
// that is already set, so the real environment always takes precedence over
// .env. A missing .env file is expected and not an error; a permission or
// I/O error, or a malformed file, is returned so a caller that intended the
// file to be read finds out at startup rather than silently running without
// it.
//
// Loading .env at all is a development convenience, not a runtime property
// of this function: a caller wires it in only for its dev environment (e.g.
// `if AppEnv() == EnvDev { errs = append(errs, LoadDotenv()...) }`), and
// should re-read AppEnv afterward, since .env may itself set APP_ENV.
func LoadDotenv() []error {
	if _, err := os.Stat(".env"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("stat .env: %w", err)}
	}
	if err := godotenv.Load(); err != nil {
		return []error{fmt.Errorf(".env: %w", err)}
	}
	return nil
}

// ServerAddrFromEnv returns the HTTP listen address derived from PORT, using
// the same parsing LoadServer uses (a leading colon is tolerated), without
// requiring a full, validated ServerConfig. It backs any first-run setup
// mode that must serve HTTP before the rest of configuration (notably a
// database DSN) exists and so cannot call the full loader chain.
func ServerAddrFromEnv() string {
	return serverAddr()
}

// trimmed reads an environment variable and trims surrounding whitespace,
// the pattern every string field in this package uses so a value consumed
// directly (a path, a URL, a key) never carries stray whitespace past
// loading.
func trimmed(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
