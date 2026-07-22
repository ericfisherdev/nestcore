package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// DBProvider selects the database backend. Both are Postgres; the provider
// only changes connectivity (TLS and pooler-safe statement handling), never
// the schema or queries.
type DBProvider string

const (
	// DBProviderPostgres is the default self-hosted Postgres backend.
	DBProviderPostgres DBProvider = "postgres"
	// DBProviderSupabase targets Supabase: Postgres reached through the
	// Supavisor connection pooler, requiring TLS and pooler-safe statement
	// handling.
	DBProviderSupabase DBProvider = "supabase"
)

// DBPoolMode declares which Supabase pooler endpoint the DSN targets. It is
// consulted only when Provider is DBProviderSupabase.
type DBPoolMode string

const (
	// DBPoolModeSession targets the session pooler or a direct connection,
	// where a backend connection is not multiplexed mid-transaction, so
	// pgx's default cached server-side prepared statements are safe.
	DBPoolModeSession DBPoolMode = "session"
	// DBPoolModeTransaction targets the transaction pooler (Supavisor port
	// 6543), which multiplexes a backend connection per transaction and is
	// incompatible with cached server-side prepared statements.
	DBPoolModeTransaction DBPoolMode = "transaction"
)

// supabaseDefaultMaxConns is the modest pool cap applied when DB_MAX_CONNS
// is unset and Provider is Supabase, because the pooler is a shared resource
// and pgx's NumCPU-based default can be too aggressive behind it.
const supabaseDefaultMaxConns int32 = 10

// DBConfig configures Postgres connectivity.
type DBConfig struct {
	// DSN is the Postgres connection string. LoadDB reads it verbatim, with
	// no environment-specific default: a caller that wants a development
	// convenience DSN applies its own fallback before calling Validate.
	DSN string
	// MaxConns caps the connection pool. Zero means "let the pool decide".
	MaxConns int32
	// ConnTimeout bounds the initial connectivity check at startup.
	ConnTimeout time.Duration
	// Provider selects the database backend (default DBProviderPostgres).
	// The Postgres path is byte-for-byte identical to before this field
	// existed.
	Provider DBProvider
	// PoolMode declares the Supabase pooler endpoint the DSN targets;
	// consulted only when Provider is DBProviderSupabase (default
	// DBPoolModeSession).
	PoolMode DBPoolMode
	// SSLRootCert is an optional path to a CA bundle. When set, the
	// connection upgrades to sslmode=verify-full and verifies the server
	// certificate against this CA.
	SSLRootCert string
	// MigrateDSN is an optional override (MIGRATE_DATABASE_URL) for the
	// connection a migration tool uses; empty means "use DSN". This lets an
	// operator point migrations at a Supabase direct/session connection
	// (port 5432) so DDL and version bookkeeping run on a session
	// connection while the app server uses the transaction pooler (port
	// 6543).
	MigrateDSN string
}

// LoadDB reads DBConfig from DATABASE_URL, DB_MAX_CONNS, DB_CONNECT_TIMEOUT,
// DB_PROVIDER, DB_POOL_MODE, DB_SSL_ROOT_CERT, and MIGRATE_DATABASE_URL.
// DATABASE_URL is read verbatim: LoadDB applies no development default, so a
// caller that wants one must apply it before calling Validate — see DSN's
// own doc for why.
func LoadDB() (DBConfig, []error) {
	var errs []error

	maxConns, err := Int32("DB_MAX_CONNS", 0)
	if err != nil {
		errs = append(errs, err)
	}
	connTimeout, err := Duration("DB_CONNECT_TIMEOUT", 5*time.Second)
	if err != nil {
		errs = append(errs, err)
	}

	// Normalized so casing/whitespace in the environment does not defeat
	// the enum validation in Validate.
	provider := DBProvider(strings.ToLower(strings.TrimSpace(String("DB_PROVIDER", string(DBProviderPostgres)))))
	poolMode := DBPoolMode(strings.ToLower(strings.TrimSpace(String("DB_POOL_MODE", string(DBPoolModeSession)))))

	// Supabase connects through a shared pooler, so default to a modest
	// pool cap when the operator has not set one. Postgres keeps deferring
	// to pgx (zero).
	if provider == DBProviderSupabase && maxConns == 0 {
		maxConns = supabaseDefaultMaxConns
	}

	return DBConfig{
		DSN:         os.Getenv("DATABASE_URL"),
		MaxConns:    maxConns,
		ConnTimeout: connTimeout,
		Provider:    provider,
		PoolMode:    poolMode,
		SSLRootCert: trimmed("DB_SSL_ROOT_CERT"),
		MigrateDSN:  trimmed("MIGRATE_DATABASE_URL"),
	}, errs
}

// Validate returns every DBConfig problem found, so callers can surface them
// together.
//
// The empty-DSN check is load-bearing, not cosmetic: pgxpool.ParseConfig("")
// does not error on an empty DSN — it resolves to libpq defaults
// (host=/tmp, port=5432, database="", user=$USER) and silently attempts a
// local Unix-socket connection as the invoking OS user. Since LoadDB applies
// no development fallback, this check is the only thing standing between an
// unset DATABASE_URL and a connection to some unrelated local Postgres.
func (d DBConfig) Validate() []error {
	var errs []error

	if strings.TrimSpace(d.DSN) == "" {
		errs = append(errs, errors.New("DATABASE_URL must not be empty"))
	}
	if d.MaxConns < 0 {
		errs = append(errs, fmt.Errorf("DB_MAX_CONNS must be >= 0, got %d", d.MaxConns))
	}
	switch d.Provider {
	case DBProviderPostgres, DBProviderSupabase:
	default:
		errs = append(errs, fmt.Errorf("DB_PROVIDER must be one of %s|%s, got %q",
			DBProviderPostgres, DBProviderSupabase, d.Provider))
	}
	switch d.PoolMode {
	case DBPoolModeSession, DBPoolModeTransaction:
	default:
		errs = append(errs, fmt.Errorf("DB_POOL_MODE must be one of %s|%s, got %q",
			DBPoolModeSession, DBPoolModeTransaction, d.PoolMode))
	}
	if d.ConnTimeout <= 0 {
		errs = append(errs, fmt.Errorf("DB_CONNECT_TIMEOUT must be positive, got %v", d.ConnTimeout))
	}

	return errs
}
