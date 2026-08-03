// Package session builds the scs.SessionManager shared by Nestova and
// Nestorage over identity.sessions (identity/migrate's 00004 migration).
// This is the SSO seam epic NSTR-112 depends on: a session written
// through the manager one app builds here is readable through the
// manager the other app builds here, because both point pgxstore at the
// SAME schema-qualified table rather than each keeping their own.
//
// # Cookie-name contract
//
// NewManager does not set Cookie.Name, leaving scs's own default,
// "session" (see the alexedwards/scs/v2 SessionCookie.Name doc). Both
// apps sharing one session MUST NOT override it independently: a
// session cookie is scoped by (host, name, path), and Tailscale's
// `tailscale serve` publishes both apps on the SAME tailnet node
// hostname (see this repo's root CLAUDE.md, "Deployment shape" —
// cookies are host-scoped and ignore port), so a mismatched cookie name
// between the two apps would make the SAME browser carry two different
// session cookies instead of one shared one, silently breaking SSO
// rather than failing loudly. Any future need to rename it must be
// coordinated as a change to THIS package, not to either app
// independently.
//
// # Cookie domain / hostname strategy
//
// Left unset here deliberately: Cookie.Domain defaults to the domain
// the cookie was issued from, which is correct for the single-node
// deployment above. Whether a future multi-node deployment needs an
// explicit Cookie.Domain is an open product decision recorded on epic
// NSTR-112, not resolved by this package.
package session

import (
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestcore/config"
)

// KeyMemberID is the session DATA key both apps MUST use to store the
// authenticated identity.member's id. This is the second half of the
// cookie-name contract above: sharing one cookie and one store is not
// enough for SSO if each app reads back a different key than the other
// wrote — a session resolvable by App A but silently anonymous in App B.
// Both apps put/get the member id under this exact key rather than a
// private literal of their own (NSTR-117).
const KeyMemberID = "member_id"

// sessionsTable schema-qualifies the pgxstore table, matching
// identity/adapter's own convention of never relying on search_path
// resolution (see identity/adapter's package doc) — pgxstore
// interpolates Config.TableName directly into its SQL, so a
// schema-qualified value works exactly like any other table reference
// in this package.
const sessionsTable = "identity.sessions"

// cleanupInterval is how often pgxstore's background goroutine sweeps
// expired rows out of identity.sessions. Both apps construct their own
// SessionManager over this SAME table, so both run this sweep
// independently — redundant but harmless (a DELETE ... WHERE expiry <
// now() is idempotent), and simpler than coordinating a single external
// job between two independently deployed binaries.
const cleanupInterval = 5 * time.Minute

// NewManager constructs an scs.SessionManager backed by Postgres over
// identity.sessions, using the pgxpool the caller already owns. Cookie
// settings are derived from cfg: Secure follows the resolved
// SESSION_COOKIE_SECURE policy (auto → prod-only, or forced true/false),
// Lifetime from SESSION_LIFETIME — see config.SessionConfig's own doc.
//
// IdleTimeout is set to half of Lifetime: active users are kept signed
// in (each request refreshes idle time) while abandoned sessions are
// reclaimed well before the hard Lifetime cap, mirroring Nestova's own
// prior session manager before this ticket lifted it into nestcore.
//
// HashTokenInStore is set so identity.sessions stores a SHA-256 digest
// of the cookie token rather than the raw token itself — the hash is a
// plain, unkeyed function of the token alone (verified against the
// installed scs v2.9.0 source), so it is safe for the SSO seam: both
// apps hash the SAME token to the SAME digest independently, with no
// shared secret or per-instance state to keep in sync. Both apps MUST
// set this identically, or a session written by one is unreadable by
// the other — the same failure mode the cookie-name contract above
// warns about.
//
// The returned stop func terminates pgxstore's background cleanup
// goroutine (started because CleanUpInterval above is non-zero).
// Long-running apps can safely ignore it — the goroutine is meant to
// run for the process lifetime. Any SHORT-LIVED caller (tests, above
// all) MUST call it once the manager is no longer needed: pgxstore's own
// docs single out exactly this case, since an uncalled stop leaves the
// goroutine running forever on a timer, sweeping a pool the caller may
// have already closed.
func NewManager(pool *pgxpool.Pool, cfg config.SessionConfig) (sm *scs.SessionManager, stop func()) {
	store := pgxstore.NewWithConfig(pool, pgxstore.Config{
		TableName:       sessionsTable,
		CleanUpInterval: cleanupInterval,
	})

	sm = scs.New()
	sm.Store = store
	sm.Lifetime = cfg.Lifetime
	sm.IdleTimeout = cfg.Lifetime / 2
	sm.HashTokenInStore = true
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Secure = cfg.Secure
	sm.Cookie.Path = "/"
	sm.Cookie.Persist = true
	return sm, store.StopCleanup
}
