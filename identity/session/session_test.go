// package session_test (an external test package) so this gated test can
// depend on nestcore/db/dbtest and nestcore/identity/migrate without
// tangling the session package's own import graph, mirroring
// identity/adapter's harness_test.go.
package session_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestcore/config"
	ncdbtest "github.com/ericfisherdev/nestcore/db/dbtest"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"
	"github.com/ericfisherdev/nestcore/identity/migrate"
	"github.com/ericfisherdev/nestcore/identity/session"
)

// harness derives isolated test databases from NESTCORE_TEST_DATABASE_URL,
// mirroring identity/adapter's own harness.
var harness = newHarness()

func newHarness() *ncdbtest.Harness {
	runner, err := migrate.New()
	if err != nil {
		panic(fmt.Sprintf("identity/session: %v", err))
	}
	return ncdbtest.New("NESTCORE_TEST_DATABASE_URL", runnerMigrator{runner})
}

// runnerMigrator adapts *ncmigrate.Runner to nestcore/db/dbtest's Migrator
// interface, mirroring identity/adapter/harness_test.go's own adapter.
type runnerMigrator struct {
	runner *ncmigrate.Runner
}

func (m runnerMigrator) Reset(ctx context.Context, dsn string) error {
	return m.runner.Reset(ctx, dsn)
}

func (m runnerMigrator) Up(ctx context.Context, dsn string) error {
	return m.runner.Up(ctx, dsn)
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestKeyMemberID_MatchesDocumentedLiteral pins KeyMemberID's value directly
// against the literal "member_id" the package doc promises both apps use.
// Without this, a rename of the constant would move the writer and reader in
// TestNewManager_SessionWrittenByOneInstanceIsReadableByAnother together and
// stay green, hiding a break with any caller still on the documented literal.
func TestKeyMemberID_MatchesDocumentedLiteral(t *testing.T) {
	if session.KeyMemberID != "member_id" {
		t.Errorf("session.KeyMemberID = %q, want %q", session.KeyMemberID, "member_id")
	}
}

// TestNewManager_SessionWrittenByOneInstanceIsReadableByAnother is the SSO
// gated proof (AC: "a session written through one SessionManager instance
// is readable through a second instance on a separate pool"): it builds
// TWO independent scs.SessionManagers, each over its OWN *pgxpool.Pool but
// both pointed at the exact same identity.sessions table — exactly how
// Nestova and Nestorage will each construct their own manager over the
// shared database. A session committed through the first must be loadable
// through the second, proving the store (not just the in-process manager)
// carries the session — the property that gives the two apps real SSO.
func TestNewManager_SessionWrittenByOneInstanceIsReadableByAnother(t *testing.T) {
	poolA := harness.NewIsolatedPool(t, "session")
	dsn := harness.DSN(t, "session")

	poolB, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect second pool: %v", err)
	}
	t.Cleanup(poolB.Close)

	cfg := config.SessionConfig{Secure: false, Lifetime: time.Hour}
	smA, stopA := session.NewManager(poolA, cfg)
	t.Cleanup(stopA)
	smB, stopB := session.NewManager(poolB, cfg)
	t.Cleanup(stopB)

	ctx := testCtx(t)

	// App A's request: start a session, store a value, commit it to get
	// the token a "cookie" would carry.
	sessCtx, err := smA.Load(ctx, "")
	if err != nil {
		t.Fatalf("smA.Load (new session): %v", err)
	}
	smA.Put(sessCtx, session.KeyMemberID, "cross-instance-test-member")
	token, _, err := smA.Commit(sessCtx)
	if err != nil {
		t.Fatalf("smA.Commit: %v", err)
	}

	// App B's request: the SAME token, loaded through a COMPLETELY
	// independent SessionManager/pool.
	otherSessCtx, err := smB.Load(ctx, token)
	if err != nil {
		t.Fatalf("smB.Load: %v", err)
	}
	got := smB.GetString(otherSessCtx, session.KeyMemberID)
	if got != "cross-instance-test-member" {
		t.Errorf("value read via the second instance = %q, want %q (SSO requires the store, not just the manager, to carry session data)", got, "cross-instance-test-member")
	}
}

// TestNewManager_CookieDefaultsDocumented pins down NewManager's cookie
// policy so a regression here is caught immediately rather than only
// discovered as a live SSO break: HttpOnly, Lax SameSite, Secure from
// config, "/" path, and Persist all matter to the cookie-name contract
// identity/session's package doc documents.
func TestNewManager_CookieDefaultsDocumented(t *testing.T) {
	pool := harness.NewIsolatedPool(t, "session")
	sm, stop := session.NewManager(pool, config.SessionConfig{Secure: true, Lifetime: 2 * time.Hour})
	t.Cleanup(stop)

	if !sm.Cookie.HttpOnly {
		t.Error("Cookie.HttpOnly = false, want true")
	}
	if !sm.Cookie.Secure {
		t.Error("Cookie.Secure = false, want true (cfg.Secure was true)")
	}
	if !sm.Cookie.Persist {
		t.Error("Cookie.Persist = false, want true")
	}
	if sm.Cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("Cookie.SameSite = %v, want SameSiteLaxMode", sm.Cookie.SameSite)
	}
	if !sm.HashTokenInStore {
		t.Error("HashTokenInStore = false, want true")
	}
	if sm.Cookie.Path != "/" {
		t.Errorf("Cookie.Path = %q, want %q", sm.Cookie.Path, "/")
	}
	if sm.Lifetime != 2*time.Hour {
		t.Errorf("Lifetime = %v, want 2h", sm.Lifetime)
	}
	if sm.IdleTimeout != time.Hour {
		t.Errorf("IdleTimeout = %v, want half of Lifetime (1h)", sm.IdleTimeout)
	}
}
