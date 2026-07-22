package config

import (
	"errors"
	"strings"
)

// devCacheDir is the default on-disk cache directory when CACHE_DIR is
// unset.
const devCacheDir = "./.localdata/cache"

// CacheConfig configures an on-disk cache for data that is derived,
// re-computable, or externally sourced. Mirrors the same
// safe-local-default-in-every-environment shape most path-valued sub-configs
// in this package share: Validate only checks Dir is non-empty, not that it
// is absolute. A relative CACHE_DIR resolves against the caller's working
// directory at the time it was launched, which can vary by how the process
// is started (systemd unit, an ad hoc shell, a container WORKDIR);
// best-effort local storage is intentional, not a startup invariant this
// package enforces. Production deployments should set CACHE_DIR to an
// absolute path so its location does not depend on how the process happens
// to be launched.
type CacheConfig struct {
	// Dir is the directory the cache opens its store under.
	Dir string
}

// LoadCache reads CacheConfig from CACHE_DIR, defaulting to devCacheDir.
func LoadCache() CacheConfig {
	return CacheConfig{Dir: strings.TrimSpace(String("CACHE_DIR", devCacheDir))}
}

// Validate returns every CacheConfig problem found, so callers can surface
// them together.
func (c CacheConfig) Validate() []error {
	if strings.TrimSpace(c.Dir) == "" {
		return []error{errors.New("CACHE_DIR must not be empty")}
	}
	return nil
}
