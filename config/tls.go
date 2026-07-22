package config

import "errors"

// TLSConfig configures optional app-terminated TLS. When both files are
// set, the caller's server listens with TLS (ListenAndServeTLS); otherwise
// it serves plain HTTP and relies on a reverse proxy for TLS. Both-or-neither
// is enforced by Validate.
type TLSConfig struct {
	// CertFile is the path to the PEM server certificate (chain).
	CertFile string
	// KeyFile is the path to the PEM private key for CertFile.
	KeyFile string
}

// Enabled reports whether app-terminated TLS is configured (both files
// present).
func (t TLSConfig) Enabled() bool {
	return t.CertFile != "" && t.KeyFile != ""
}

// LoadTLS reads TLSConfig from TLS_CERT_FILE and TLS_KEY_FILE.
func LoadTLS() TLSConfig {
	return TLSConfig{
		CertFile: trimmed("TLS_CERT_FILE"),
		KeyFile:  trimmed("TLS_KEY_FILE"),
	}
}

// Validate returns every TLSConfig problem found, so callers can surface
// them together. This is the same both-or-neither shape as
// validateCredentialPair, but is not built on it: unlike S3/SMS/Email's
// credential pairs, an unset TLS pair has no fallback credential chain to
// mention, so the message differs.
func (t TLSConfig) Validate() []error {
	if (t.CertFile == "") != (t.KeyFile == "") {
		return []error{errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together (or both unset)")}
	}
	return nil
}
