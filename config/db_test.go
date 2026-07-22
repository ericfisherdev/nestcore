package config_test

import (
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadDB(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.DBConfig
	}{
		{
			name: "defaults when empty",
			env:  map[string]string{},
			want: config.DBConfig{ConnTimeout: 5 * time.Second, Provider: config.DBProviderPostgres, PoolMode: config.DBPoolModeSession},
		},
		{
			name: "DATABASE_URL is read verbatim with no dev default",
			env:  map[string]string{"DATABASE_URL": "postgres://custom:pwd@dbhost:5432/mydb"},
			want: config.DBConfig{DSN: "postgres://custom:pwd@dbhost:5432/mydb", ConnTimeout: 5 * time.Second, Provider: config.DBProviderPostgres, PoolMode: config.DBPoolModeSession},
		},
		{
			name: "parsed numeric and duration overrides",
			env:  map[string]string{"DB_MAX_CONNS": "10", "DB_CONNECT_TIMEOUT": "2s"},
			want: config.DBConfig{MaxConns: 10, ConnTimeout: 2 * time.Second, Provider: config.DBProviderPostgres, PoolMode: config.DBPoolModeSession},
		},
		{
			name: "supabase provider applies a default pool cap and session mode",
			env:  map[string]string{"DB_PROVIDER": "supabase"},
			want: config.DBConfig{ConnTimeout: 5 * time.Second, MaxConns: 10, Provider: config.DBProviderSupabase, PoolMode: config.DBPoolModeSession},
		},
		{
			// Case-insensitive provider, transaction pool mode, an explicit
			// pool cap that overrides the supabase default, and a root cert
			// path.
			name: "supabase transaction mode with explicit cap and root cert",
			env: map[string]string{
				"DB_PROVIDER": "Supabase", "DB_POOL_MODE": "transaction",
				"DB_MAX_CONNS": "5", "DB_SSL_ROOT_CERT": "/etc/ssl/ca.crt",
			},
			want: config.DBConfig{
				ConnTimeout: 5 * time.Second, MaxConns: 5, Provider: config.DBProviderSupabase,
				PoolMode: config.DBPoolModeTransaction, SSLRootCert: "/etc/ssl/ca.crt",
			},
		},
		{
			// MIGRATE_DATABASE_URL is captured separately so migrations can
			// target a session/direct connection while the app uses the
			// transaction pooler.
			name: "separate migrate DSN is captured",
			env: map[string]string{
				"DB_PROVIDER": "supabase", "DB_POOL_MODE": "transaction",
				"DATABASE_URL": "postgres://u:p@pooler:6543/postgres", "MIGRATE_DATABASE_URL": "postgres://u:p@db:5432/postgres",
			},
			want: config.DBConfig{
				DSN: "postgres://u:p@pooler:6543/postgres", ConnTimeout: 5 * time.Second, MaxConns: 10,
				Provider: config.DBProviderSupabase, PoolMode: config.DBPoolModeTransaction, MigrateDSN: "postgres://u:p@db:5432/postgres",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadDB()
			if len(errs) > 0 {
				t.Fatalf("LoadDB() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadDB() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadDBInvalid(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantContains []string
	}{
		{name: "non-integer DB_MAX_CONNS", env: map[string]string{"DB_MAX_CONNS": "abc"}, wantContains: []string{"DB_MAX_CONNS"}},
		{name: "invalid duration", env: map[string]string{"DB_CONNECT_TIMEOUT": "5x"}, wantContains: []string{"DB_CONNECT_TIMEOUT"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			_, errs := config.LoadDB()
			if len(errs) == 0 {
				t.Fatal("LoadDB() = no errors, want an error")
			}
			joined := errsToString(errs)
			for _, want := range tt.wantContains {
				if !contains(joined, want) {
					t.Errorf("LoadDB() errors = %q, want it to contain %q", joined, want)
				}
			}
		})
	}
}

func TestDBConfigValidate(t *testing.T) {
	valid := config.DBConfig{
		DSN: "postgres://u:p@db:5432/app", ConnTimeout: 5 * time.Second,
		Provider: config.DBProviderPostgres, PoolMode: config.DBPoolModeSession,
	}

	tests := []struct {
		name         string
		mutate       func(config.DBConfig) config.DBConfig
		wantContains []string
	}{
		{
			name:   "valid config passes",
			mutate: func(c config.DBConfig) config.DBConfig { return c },
		},
		{
			// Load-bearing: pgxpool.ParseConfig("") does not error on an
			// empty DSN, it silently resolves to libpq defaults and
			// attempts a local Unix-socket connection. This is the only
			// thing standing between an unset DATABASE_URL and that.
			name: "empty DSN is rejected",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.DSN = ""
				return c
			},
			wantContains: []string{"DATABASE_URL", "must not be empty"},
		},
		{
			name: "whitespace-only DSN is rejected",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.DSN = "   "
				return c
			},
			wantContains: []string{"DATABASE_URL"},
		},
		{
			name: "negative max conns",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.MaxConns = -1
				return c
			},
			wantContains: []string{"DB_MAX_CONNS", ">= 0"},
		},
		{
			name: "invalid provider",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.Provider = "mysql"
				return c
			},
			wantContains: []string{"DB_PROVIDER"},
		},
		{
			name: "invalid pool mode",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.PoolMode = "statement"
				return c
			},
			wantContains: []string{"DB_POOL_MODE"},
		},
		{
			name: "non-positive connect timeout",
			mutate: func(c config.DBConfig) config.DBConfig {
				c.ConnTimeout = 0
				return c
			},
			wantContains: []string{"DB_CONNECT_TIMEOUT", "positive"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.mutate(valid).Validate()
			if len(tt.wantContains) == 0 {
				if len(errs) > 0 {
					t.Errorf("Validate() = %v, want no errors", errs)
				}
				return
			}
			joined := errsToString(errs)
			for _, want := range tt.wantContains {
				if !contains(joined, want) {
					t.Errorf("Validate() = %q, want it to contain %q", joined, want)
				}
			}
		})
	}
}
