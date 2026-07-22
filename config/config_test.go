package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestAppEnv(t *testing.T) {
	t.Run("unset defaults to dev", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if got := config.AppEnv(); got != config.EnvDev {
			t.Errorf("AppEnv() = %q, want %q", got, config.EnvDev)
		}
	})
	t.Run("reads an explicit value", func(t *testing.T) {
		setEnv(t, map[string]string{"APP_ENV": "prod"})
		if got := config.AppEnv(); got != config.EnvProd {
			t.Errorf("AppEnv() = %q, want %q", got, config.EnvProd)
		}
	})
}

func TestValidateAppEnv(t *testing.T) {
	for _, env := range []string{config.EnvDev, config.EnvTest, config.EnvProd} {
		if errs := config.ValidateAppEnv(env); len(errs) > 0 {
			t.Errorf("ValidateAppEnv(%q) = %v, want no errors", env, errs)
		}
	}
	errs := config.ValidateAppEnv("staging")
	if len(errs) == 0 || !contains(errsToString(errs), "APP_ENV") {
		t.Errorf("ValidateAppEnv(%q) = %v, want an APP_ENV error", "staging", errs)
	}
}

func TestServerAddrFromEnv(t *testing.T) {
	t.Run("defaults to 8080", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if got := config.ServerAddrFromEnv(); got != ":8080" {
			t.Errorf("ServerAddrFromEnv() = %q, want :8080", got)
		}
	})
	t.Run("tolerates a leading colon", func(t *testing.T) {
		setEnv(t, map[string]string{"PORT": ":3000"})
		if got := config.ServerAddrFromEnv(); got != ":3000" {
			t.Errorf("ServerAddrFromEnv() = %q, want :3000", got)
		}
	})
}

func TestLoadDotenv(t *testing.T) {
	t.Run("missing .env is not an error", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if errs := config.LoadDotenv(); errs != nil {
			t.Errorf("LoadDotenv() = %v, want nil", errs)
		}
	})
	t.Run("valid .env sets an unset variable", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if err := os.WriteFile(".env", []byte("NESTCORE_TEST_DOTENV=from-file\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("NESTCORE_TEST_DOTENV", "")
		_ = os.Unsetenv("NESTCORE_TEST_DOTENV")
		if errs := config.LoadDotenv(); errs != nil {
			t.Fatalf("LoadDotenv() = %v, want nil", errs)
		}
		if got := os.Getenv("NESTCORE_TEST_DOTENV"); got != "from-file" {
			t.Errorf("NESTCORE_TEST_DOTENV = %q, want from-file", got)
		}
	})
	t.Run("real environment wins over .env", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if err := os.WriteFile(".env", []byte("NESTCORE_TEST_DOTENV=from-file\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("NESTCORE_TEST_DOTENV", "from-environment")
		if errs := config.LoadDotenv(); errs != nil {
			t.Fatalf("LoadDotenv() = %v, want nil", errs)
		}
		if got := os.Getenv("NESTCORE_TEST_DOTENV"); got != "from-environment" {
			t.Errorf("NESTCORE_TEST_DOTENV = %q, want the real environment to win", got)
		}
	})
	t.Run("malformed .env is surfaced", func(t *testing.T) {
		setEnv(t, map[string]string{})
		if err := os.WriteFile(".env", []byte(`FOO="unterminated`), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		errs := config.LoadDotenv()
		if len(errs) == 0 {
			t.Fatal("LoadDotenv() = nil, want an error for a malformed .env")
		}
	})
}

// appConfig mirrors the shape of an application's own root configuration
// struct: named fields holding nestcore sub-configs, plus the app's own
// deployment Env, exactly the pattern a consuming application's own
// migration rewires its root config onto. It is declared here, in
// nestcore's own test suite, to prove the exported API is sufficient for
// that composition before any consumer depends on it.
type appConfig struct {
	Env     string
	Server  config.ServerConfig
	DB      config.DBConfig
	Session config.SessionConfig
	Crypto  config.CryptoConfig
	TLS     config.TLSConfig
	HSTS    config.HSTSConfig
	S3      config.S3Config
	SMS     config.SMSConfig
	Email   config.EmailConfig
	Cache   config.CacheConfig
}

// composeAppConfig loads and validates every nestcore sub-config the way an
// application's own Load() would: resolving the dev-only .env and dev DSN
// fallback itself (both stay out of nestcore), and caller-gating S3 on its
// own storage-backend selection (s3Enabled) the way S3Config.Validate's own
// doc requires.
func composeAppConfig(s3Enabled bool) (appConfig, []error) {
	var errs []error

	env := config.AppEnv()
	if env == config.EnvDev {
		errs = append(errs, config.LoadDotenv()...)
		env = config.AppEnv() // re-read: .env may itself set APP_ENV
	}
	errs = append(errs, config.ValidateAppEnv(env)...)

	server, serverErrs := config.LoadServer()
	errs = append(errs, serverErrs...)
	errs = append(errs, server.Validate()...)

	db, dbErrs := config.LoadDB()
	errs = append(errs, dbErrs...)
	// An app's own dev-only DSN convenience default, applied before
	// Validate — LoadDB deliberately carries none, see DBConfig.DSN's doc.
	if db.DSN == "" && env == config.EnvDev {
		db.DSN = "postgres://app:app@localhost:5432/app_dev?sslmode=disable"
	}
	errs = append(errs, db.Validate()...)

	session, sessionErrs := config.LoadSession(env)
	errs = append(errs, sessionErrs...)
	errs = append(errs, session.Validate(env)...)

	crypto := config.LoadCrypto()
	errs = append(errs, crypto.Validate(env)...)

	tls := config.LoadTLS()
	errs = append(errs, tls.Validate()...)

	hsts, hstsErrs := config.LoadHSTS()
	errs = append(errs, hstsErrs...)
	errs = append(errs, hsts.Validate()...)

	// S3 is caller-gated: only append LoadS3/Validate's findings when this
	// app's own storage-backend selection actually opted into S3.
	s3, s3Errs := config.LoadS3()
	if s3Enabled {
		errs = append(errs, s3Errs...)
		errs = append(errs, s3.Validate()...)
	}

	sms, smsErrs := config.LoadSMS()
	errs = append(errs, smsErrs...)
	errs = append(errs, sms.Validate()...)

	email, emailErrs := config.LoadEmail()
	errs = append(errs, emailErrs...)
	errs = append(errs, email.Validate()...)

	cache := config.LoadCache()
	errs = append(errs, cache.Validate()...)

	return appConfig{
		Env: env, Server: server, DB: db, Session: session, Crypto: crypto,
		TLS: tls, HSTS: hsts, S3: s3, SMS: sms, Email: email, Cache: cache,
	}, errs
}

func TestComposeAppShapedConfig(t *testing.T) {
	t.Run("a valid dev deployment composes with no errors", func(t *testing.T) {
		setEnv(t, map[string]string{})
		got, errs := composeAppConfig(false)
		if len(errs) > 0 {
			t.Fatalf("composeAppConfig() unexpected errors: %v", errs)
		}
		if got.Env != config.EnvDev {
			t.Errorf("Env = %q, want %q", got.Env, config.EnvDev)
		}
		if got.DB.DSN == "" {
			t.Error("DB.DSN is empty, want the app's own dev fallback to have applied")
		}
	})

	t.Run("a valid prod deployment composes with no errors", func(t *testing.T) {
		setEnv(t, map[string]string{
			"APP_ENV":        config.EnvProd,
			"DATABASE_URL":   "postgres://u:p@db:5432/app",
			"SESSION_SECRET": strings.Repeat("a", 32),
			"ENCRYPTION_KEY": validEncryptionKey,
		})
		_, errs := composeAppConfig(false)
		if len(errs) > 0 {
			t.Fatalf("composeAppConfig() unexpected errors: %v", errs)
		}
	})

	t.Run("s3 findings are gated on the caller's own selection", func(t *testing.T) {
		setEnv(t, map[string]string{"S3_ACCESS_KEY_ID": "minioadmin"}) // partial, invalid if it counted
		if _, errs := composeAppConfig(false); len(errs) > 0 {
			t.Fatalf("composeAppConfig(s3Enabled=false) unexpected errors: %v", errs)
		}
		if _, errs := composeAppConfig(true); len(errs) == 0 {
			t.Fatal("composeAppConfig(s3Enabled=true) = no errors, want the partial S3 credentials to be reported")
		}
	})

	t.Run("aggregates problems across every sub-config in one pass", func(t *testing.T) {
		setEnv(t, map[string]string{
			"APP_ENV":        "staging",
			"DB_MAX_CONNS":   "nope",
			"SESSION_SECRET": "x",
		})
		_, errs := composeAppConfig(false)
		joined := errsToString(errs)
		for _, want := range []string{"APP_ENV", "DB_MAX_CONNS", "SESSION_SECRET"} {
			if !contains(joined, want) {
				t.Errorf("composeAppConfig() errors = %q, want it to contain %q", joined, want)
			}
		}
	})
}
