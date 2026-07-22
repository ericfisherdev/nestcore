package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// String returns the value of the environment variable named key, or
// fallback when the variable is unset or empty.
func String(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// Int32 parses an int32 environment variable, returning fallback when unset
// or empty and an error when present but not a valid integer.
func Int32(key string, fallback int32) (int32, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		// Return the fallback alongside the error so the caller still holds a
		// sane value and downstream validation does not double-report this
		// field.
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return int32(n), nil
}

// Int64 parses an int64 environment variable, returning fallback when unset
// or empty and an error when present but not a valid integer.
func Int64(key string, fallback int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		// Return the fallback alongside the error so the caller still holds a
		// sane value and downstream validation does not double-report this
		// field.
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}

// Duration parses a duration environment variable (e.g. "30s", "5m"),
// returning fallback when unset or empty and an error when present but
// invalid.
func Duration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		// Return the fallback alongside the error so the caller still holds a
		// sane value and downstream validation does not double-report this
		// field.
		return fallback, fmt.Errorf("%s must be a duration (e.g. 30s, 5m): %w", key, err)
	}
	return d, nil
}

// Bool parses a boolean environment variable via strconv.ParseBool, which
// accepts 1/t/T/TRUE/true/True and 0/f/F/FALSE/false/False (not an arbitrary
// mixed case like "tRuE"), returning fallback when unset or empty and an
// error when present but invalid.
func Bool(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		// Return the fallback alongside the error so the caller still holds a
		// sane value and downstream validation does not double-report this
		// field.
		return fallback, fmt.Errorf("%s must be a boolean (true/false): %w", key, err)
	}
	return b, nil
}
