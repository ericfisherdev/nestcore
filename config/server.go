package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// defaultTrustedProxies is the TRUSTED_PROXIES default: loopback only,
	// on the assumption that a same-host reverse proxy is the common case.
	// It applies only when TRUSTED_PROXIES is unset; an explicit empty value
	// trusts no proxy and ignores forwarded headers entirely.
	defaultTrustedProxies = "127.0.0.0/8,::1/128"

	// defaultServerRequestTimeout is SERVER_REQUEST_TIMEOUT's default:
	// generous enough for the slowest legitimate request a deployment
	// handles (e.g. a large upload over a weak connection), not tuned to a
	// fast-LAN-only assumption.
	defaultServerRequestTimeout = 120 * time.Second
	// minServerRequestTimeout is the floor LoadServer enforces for
	// SERVER_REQUEST_TIMEOUT: below this, ordinary requests (not just large
	// uploads) risk spurious timeouts, and a caller's derived per-request
	// context deadline (request timeout minus its own margin) could be
	// squeezed uncomfortably thin.
	minServerRequestTimeout = 15 * time.Second
)

// ServerConfig configures the HTTP listener.
type ServerConfig struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// TrustedProxies is the raw, comma-separated CIDR list (from
	// TRUSTED_PROXIES) of reverse-proxy source networks whose
	// X-Forwarded-* headers are trusted. It is validated at Validate; call
	// TrustedProxyPrefixes for the parsed form. Forwarded headers should be
	// honored only when the immediate peer falls inside one of these
	// networks, so an external client cannot spoof a secure context. An
	// empty value trusts no proxy.
	TrustedProxies string
	// RequestTimeout bounds how long the server allows a single request to
	// take end to end — both the connection-level ReadTimeout/WriteTimeout
	// and (minus a small margin) the per-request context deadline applied
	// to every handler.
	RequestTimeout time.Duration
	// PublicBaseURL is the externally-reachable origin (scheme + host, no
	// trailing slash, e.g. "https://app.tailxxxx.ts.net") a caller builds
	// absolute links against. Empty (the default) means "derive it from the
	// incoming request" instead.
	//
	// A caller that pins a fixed Relying Party ID for WebAuthn, or anything
	// else that cannot tolerate a per-request derived origin, additionally
	// REQUIRES this to be set: changing PublicBaseURL's host after such an
	// identity has been registered against it breaks every credential
	// registered under the old value, since the value is baked in at
	// registration time, not re-derived per request.
	PublicBaseURL string
}

// TrustedProxyPrefixes parses TrustedProxies into netip prefixes for a
// forwarded-headers middleware. TrustedProxies is validated during Validate,
// so any malformed entry would already have failed startup; this drops such
// entries defensively and never returns an error.
func (s ServerConfig) TrustedProxyPrefixes() []netip.Prefix {
	prefixes, _ := parseTrustedProxies(s.TrustedProxies)
	return prefixes
}

// serverAddr parses PORT into a listen address, tolerating a leading colon
// (e.g. PORT=":8080") so it does not produce a malformed "::8080" address.
// Shared by LoadServer and ServerAddrFromEnv so both derive the address the
// same way.
func serverAddr() string {
	return ":" + strings.TrimPrefix(String("PORT", "8080"), ":")
}

// LoadServer reads ServerConfig from PORT, TRUSTED_PROXIES,
// SERVER_REQUEST_TIMEOUT, and PUBLIC_BASE_URL.
func LoadServer() (ServerConfig, []error) {
	var errs []error

	requestTimeout, err := Duration("SERVER_REQUEST_TIMEOUT", defaultServerRequestTimeout)
	if err != nil {
		errs = append(errs, err)
	}

	// TRUSTED_PROXIES: LookupEnv distinguishes "unset" (apply the loopback
	// default) from an explicit empty value (trust nothing, ignoring
	// forwarded headers). The raw value is stored on ServerConfig; Validate
	// checks it is parseable, matching every other syntactic check in this
	// package.
	trustedProxies, ok := os.LookupEnv("TRUSTED_PROXIES")
	if !ok {
		trustedProxies = defaultTrustedProxies
	}

	// PUBLIC_BASE_URL: TrimRight (not TrimSuffix, which removes only ONE
	// occurrence) strips EVERY trailing slash, so an operator's accidental
	// "https://host//" does not survive as a single leftover slash and
	// double up with the leading slash on every concatenated link path.
	publicBaseURL := strings.TrimRight(trimmed("PUBLIC_BASE_URL"), "/")

	return ServerConfig{
		Addr:           serverAddr(),
		TrustedProxies: trustedProxies,
		RequestTimeout: requestTimeout,
		PublicBaseURL:  publicBaseURL,
	}, errs
}

// Validate returns every ServerConfig problem found, so callers can surface
// them together.
func (s ServerConfig) Validate() []error {
	var errs []error

	if s.RequestTimeout < minServerRequestTimeout {
		errs = append(errs, fmt.Errorf("SERVER_REQUEST_TIMEOUT must be at least %v, got %v",
			minServerRequestTimeout, s.RequestTimeout))
	}

	if _, err := parseTrustedProxies(s.TrustedProxies); err != nil {
		errs = append(errs, err)
	}

	// PUBLIC_BASE_URL is optional (empty means "derive from the request"),
	// but when set it must be an origin ONLY — scheme + host(:port),
	// nothing else, not even a bare trailing slash — so it can be
	// concatenated directly with a link path with no further validation,
	// normalization, or path-joining logic at request time. Userinfo, a
	// path (including "/"), a query, or a fragment would all either be
	// silently discarded (surprising) or double up with the link path
	// (broken); rejecting them here catches an operator's copy-paste
	// mistake before it reaches production.
	//
	// The bare-"/" case is deliberately rejected too, not exempted: a
	// caller that matches this value against a browser-reported origin via
	// exact string comparison (e.g. WebAuthn's RPOrigins) would otherwise
	// have "https://example.org/" silently never match
	// "https://example.org" — an origin never has a trailing slash.
	if s.PublicBaseURL != "" {
		u, err := url.Parse(s.PublicBaseURL)
		switch {
		case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Host == "":
			errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL must be an absolute http(s) URL, got %q", s.PublicBaseURL))
		case u.User != nil, u.Path != "", u.RawQuery != "", u.Fragment != "":
			errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL must be an origin only (scheme + host, no user/path/query/fragment), got %q", s.PublicBaseURL))
		}
	}

	return errs
}

// parseTrustedProxies parses a comma-separated CIDR list (e.g.
// "127.0.0.0/8,::1/128") into masked netip prefixes. Empty or
// whitespace-only entries are skipped, so an empty value yields no prefixes
// (trust nothing). Every malformed entry is reported so the operator can fix
// them in one pass.
func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	fields := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(fields))
	var errs []error
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES entry %q is not a valid CIDR: %w", f, err))
			continue
		}
		// Mask so host bits are zeroed: it normalizes the value for
		// containment checks and makes the parsed result stable regardless
		// of how it was written.
		prefixes = append(prefixes, p.Masked())
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return prefixes, nil
}
