package config

import "fmt"

// validateCredentialPair returns an error when exactly one of a paired set
// of static credential fields is set — a lone access key or secret is
// always a misconfiguration, never a valid partial state. Shared by
// S3Config, SMSConfig, and EmailConfig, whose credential pairs all fall
// back to the same default AWS credential chain when both are left unset.
func validateCredentialPair(idKey, id, secretKey, secret string) error {
	if (id == "") != (secret == "") {
		return fmt.Errorf("%s and %s must be set together (or both left unset to use the default AWS credential chain)", idKey, secretKey)
	}
	return nil
}
