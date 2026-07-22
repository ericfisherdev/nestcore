package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// DefaultHSTSMaxAge is the HSTS max-age applied when HSTS is enabled
// without an explicit HSTS_MAX_AGE (~180 days) — long enough to be
// effective, short of the 1-year preload-list minimum so it stays low-risk.
const DefaultHSTSMaxAge = 180 * 24 * time.Hour

// hstsPreloadMinMaxAge is the minimum max-age the HSTS preload list
// requires.
const hstsPreloadMinMaxAge = 365 * 24 * time.Hour

// HSTSConfig configures the HTTP Strict-Transport-Security response header.
// HSTS is opt-in because it is sticky and hard to undo, so it should only be
// enabled on a stable HTTPS hostname, and emitted only over HTTPS.
type HSTSConfig struct {
	// Enabled turns the Strict-Transport-Security header on.
	Enabled bool
	// MaxAge is the max-age directive (emitted as whole seconds). It is
	// only meaningful when MaxAgeSet is true; see EffectiveMaxAge.
	MaxAge time.Duration
	// MaxAgeSet records whether HSTS_MAX_AGE was explicitly provided. It
	// lets an explicit max-age=0 (which clears a previously-sent HSTS
	// policy in browsers) be distinguished from "unset" (apply
	// DefaultHSTSMaxAge). A negative explicit value is invalid.
	MaxAgeSet bool
	// IncludeSubdomains adds the includeSubDomains directive.
	IncludeSubdomains bool
	// Preload adds the preload directive (requires includeSubDomains +
	// max-age >= 1y).
	Preload bool
}

// EffectiveMaxAge returns the max-age the header should carry: the explicit
// value when HSTS_MAX_AGE was set (including 0 to clear HSTS), otherwise
// DefaultHSTSMaxAge.
func (h HSTSConfig) EffectiveMaxAge() time.Duration {
	if !h.MaxAgeSet {
		return DefaultHSTSMaxAge
	}
	return h.MaxAge
}

// LoadHSTS reads HSTSConfig from HSTS_ENABLED, HSTS_MAX_AGE,
// HSTS_INCLUDE_SUBDOMAINS, and HSTS_PRELOAD.
func LoadHSTS() (HSTSConfig, []error) {
	var errs []error

	enabled, err := Bool("HSTS_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	// Track whether HSTS_MAX_AGE was set explicitly so an explicit 0 (clear
	// HSTS) is distinct from "unset" (apply DefaultHSTSMaxAge). Duration
	// returns 0 for both, so LookupEnv is what disambiguates.
	maxAge, err := Duration("HSTS_MAX_AGE", 0)
	if err != nil {
		errs = append(errs, err)
	}
	maxAgeRaw, maxAgeOK := os.LookupEnv("HSTS_MAX_AGE")
	maxAgeSet := maxAgeOK && maxAgeRaw != ""
	includeSubdomains, err := Bool("HSTS_INCLUDE_SUBDOMAINS", false)
	if err != nil {
		errs = append(errs, err)
	}
	preload, err := Bool("HSTS_PRELOAD", false)
	if err != nil {
		errs = append(errs, err)
	}

	return HSTSConfig{
		Enabled:           enabled,
		MaxAge:            maxAge,
		MaxAgeSet:         maxAgeSet,
		IncludeSubdomains: includeSubdomains,
		Preload:           preload,
	}, errs
}

// Validate returns every HSTSConfig problem found, so callers can surface
// them together.
func (h HSTSConfig) Validate() []error {
	var errs []error

	// A negative max-age is invalid; zero is allowed and means "use the
	// built-in default". (max-age=0 to expire HSTS is achieved by simply
	// disabling it via HSTS_ENABLED.)
	if h.Enabled && h.MaxAgeSet && h.MaxAge < 0 {
		errs = append(errs, fmt.Errorf("HSTS_MAX_AGE must not be negative, got %v", h.MaxAge))
	}
	// The preload directive is a public commitment with strict
	// requirements: the HSTS preload list requires includeSubDomains and
	// max-age >= 1 year. Reject a preload config that browsers' preload
	// submission would, so it is caught at startup rather than after a
	// hard-to-undo deployment.
	if h.Enabled && h.Preload {
		if h.EffectiveMaxAge() < hstsPreloadMinMaxAge {
			errs = append(errs, fmt.Errorf("HSTS_PRELOAD requires HSTS_MAX_AGE >= 1 year, got %v", h.EffectiveMaxAge()))
		}
		if !h.IncludeSubdomains {
			errs = append(errs, errors.New("HSTS_PRELOAD requires HSTS_INCLUDE_SUBDOMAINS=true"))
		}
	}

	return errs
}
