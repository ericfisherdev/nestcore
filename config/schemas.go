package config

import (
	"fmt"
	"regexp"
	"strings"
)

// SchemaConfig names the three Postgres schemas the shared "nest" database
// holds: Identity (owned by nestcore, shared by every app), and one schema
// per app for its own tables. Every field defaults to the canonical
// install's name, so a single-database, single-app deployment needs none of
// this section's environment variables set.
//
// Identity's default ("identity") is the only value nestcore/identity/migrate
// actually consumes today — that package's schema name is still a compile-time
// constant, not wired to this config. An operator who overrides
// DB_SCHEMA_IDENTITY changes what this config reports, but not what
// identity/migrate migrates against, until a future ticket parameterizes it.
type SchemaConfig struct {
	// Identity is the shared schema nestcore's identity package owns:
	// household, member, credentials, and sessions.
	Identity string
	// Nestova is Nestova's own schema for its non-identity tables.
	Nestova string
	// Nestorage is Nestorage's own schema for its non-identity tables.
	Nestorage string
}

// Canonical default schema names, applied when the corresponding
// DB_SCHEMA_* variable is unset — the shape a fresh install needs zero
// configuration to reach.
const (
	defaultIdentitySchema  = "identity"
	defaultNestovaSchema   = "nestova"
	defaultNestorageSchema = "nestorage"
)

// LoadSchemas reads SchemaConfig from DB_SCHEMA_IDENTITY, DB_SCHEMA_NESTOVA,
// and DB_SCHEMA_NESTORAGE, defaulting to identity, nestova, and nestorage.
func LoadSchemas() SchemaConfig {
	return SchemaConfig{
		// Trimmed for the same reason LoadDB trims DB_PROVIDER/DB_POOL_MODE:
		// whitespace in the environment must not defeat Validate's identifier
		// check.
		Identity:  strings.TrimSpace(String("DB_SCHEMA_IDENTITY", defaultIdentitySchema)),
		Nestova:   strings.TrimSpace(String("DB_SCHEMA_NESTOVA", defaultNestovaSchema)),
		Nestorage: strings.TrimSpace(String("DB_SCHEMA_NESTORAGE", defaultNestorageSchema)),
	}
}

// schemaIdentifier matches a plain lowercase Postgres identifier: lowercase
// letters, digits, and underscores, not starting with a digit — the subset
// that never needs quoting, so a configured schema name is always safe to
// splice into DDL unquoted.
var schemaIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// maxIdentifierBytes is Postgres's identifier length limit; a longer name is
// silently truncated by the server, which would let two distinct configured
// names collide on the same physical schema.
const maxIdentifierBytes = 63

// Validate returns every SchemaConfig problem found, so callers can surface
// them together: each name must be a valid, unquoted Postgres identifier of
// no more than 63 bytes, and the three names must be pairwise distinct.
func (s SchemaConfig) Validate() []error {
	var errs []error

	errs = append(errs, validateSchemaName("DB_SCHEMA_IDENTITY", s.Identity)...)
	errs = append(errs, validateSchemaName("DB_SCHEMA_NESTOVA", s.Nestova)...)
	errs = append(errs, validateSchemaName("DB_SCHEMA_NESTORAGE", s.Nestorage)...)

	if s.Identity == s.Nestova {
		errs = append(errs, fmt.Errorf("DB_SCHEMA_IDENTITY and DB_SCHEMA_NESTOVA must not be the same schema (both %q)", s.Identity))
	}
	if s.Identity == s.Nestorage {
		errs = append(errs, fmt.Errorf("DB_SCHEMA_IDENTITY and DB_SCHEMA_NESTORAGE must not be the same schema (both %q)", s.Identity))
	}
	if s.Nestova == s.Nestorage {
		errs = append(errs, fmt.Errorf("DB_SCHEMA_NESTOVA and DB_SCHEMA_NESTORAGE must not be the same schema (both %q)", s.Nestova))
	}

	return errs
}

// validateSchemaName checks a single schema name against the identifier
// pattern and length limit, naming key in every error so Validate's
// aggregated report is actionable per-variable.
func validateSchemaName(key, name string) []error {
	var errs []error
	if !schemaIdentifier.MatchString(name) {
		errs = append(errs, fmt.Errorf("%s must be a lowercase Postgres identifier (letters, digits, underscore, not starting with a digit), got %q", key, name))
	}
	if len(name) > maxIdentifierBytes {
		errs = append(errs, fmt.Errorf("%s must be at most %d bytes, got %d", key, maxIdentifierBytes, len(name)))
	}
	return errs
}
