// Package migrate owns nestcore's identity schema: household, member (with
// its unified owner/adult/child role vocabulary and IDENTITY-level active
// flag), the MFA/recovery-code/WebAuthn credential tables, and the shared
// sessions table that gives Nestova and Nestorage one login (epic
// NSTR-112). Applying, rolling back, and inspecting migrations is entirely
// nestcore/db/migrate's job; this package only supplies the embedded
// migration set, this schema's version-table/ensure-schema/session-lock
// configuration, and the minimum-schema-version boot guard both apps call.
//
// # Migration ownership
//
// Two independently versioned binaries — Nestova and Nestorage — share this
// schema, so neither app may own its migrations: nestcore ships this
// embedded set with its own goose version table, identity.goose_db_version,
// distinct from either app's own; each app runs this set at startup under a
// Postgres session-level advisory lock (New enables it), so two apps
// booting concurrently cannot race the same DDL; and each app calls
// RequireVersion with the version it was built against, refusing to boot
// against an identity schema older than that.
//
// Operator prerequisite: both apps must reach this schema as the SAME
// Postgres role, or the identity schema must be created and granted out of
// band before either boots. New creates the schema implicitly (CREATE SCHEMA
// IF NOT EXISTS) as whichever role connects first, and that role owns it;
// this package issues no GRANTs, so a second app connecting as a different
// role fails on goose's own CREATE TABLE IF NOT EXISTS
// identity.goose_db_version with "permission denied for schema identity",
// before any migration here runs.
//
// This forces an ADDITIVE-ONLY discipline on every migration in this
// package: whichever app deploys first migrates identity forward, and the
// other app — built against the older schema — must keep working against
// the newer one. Identity migrations may therefore only add new tables, new
// nullable columns, or widened constraints. Never renames, drops, type
// changes, or narrowed checks. RequireVersion's minimum-version check
// protects the too-old direction; additive-only discipline protects the
// too-new direction, since nothing about the schema an older binary expects
// is ever removed or changed underneath it.
//
// # App-side presentation boundary
//
// Presentation fields stay OUT of identity: Nestova's member color_key and
// Nestorage's member color live in each app's own member-profile table,
// keyed on identity.member's id. The `active` column on identity.member is
// the deliberate exception to that boundary: it is lifecycle, not
// presentation, so it lives here rather than app-side. Deactivating a
// member cuts access to BOTH apps at once — session validation, login, and
// Nestorage's device-token and API-key resolution all read this shared
// flag.
//
// # Deactivation, not deletion
//
// A member row is never deleted. Many app migrations FK-reference member
// (tasks, gamification, chore trades, tracking, calendar, MFA, WebAuthn
// credentials, notification preferences, and Nestorage's storage tables), so
// removal is modeled as setting active to false, which every piece of
// history keeps its attribution against. The operations and guards that
// consume the active flag ship in NSTR-111, not in this package.
package migrate

import (
	"context"
	"embed"
	"fmt"

	dbmigrate "github.com/ericfisherdev/nestcore/db/migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// SourceDir is the on-disk migrations directory relative to the repo root,
// the conventional location a migration-scaffolding command would write new
// files into.
const SourceDir = "identity/migrate/migrations"

// versionTable is identity's own goose bookkeeping table, schema-qualified
// so it lives beside the tables it tracks instead of colliding with either
// app's own goose_db_version.
const versionTable = "identity.goose_db_version"

// schemaName is the Postgres schema every identity migration's DDL is
// qualified against.
const schemaName = "identity"

// New returns a Runner over nestcore's embedded identity migration set,
// configured with identity's own version table, an ensure-schema guard (so
// goose never fails creating that version table against a schema that does
// not exist yet on a fresh database), and a session-level advisory lock —
// see the package doc's Migration ownership section for why all three are
// required here specifically. RequireVersion deliberately builds its own
// Runner rather than calling New, to leave the session lock out — see its
// own doc comment.
func New() (*dbmigrate.Runner, error) {
	return dbmigrate.New(migrationsFS, "migrations",
		dbmigrate.WithVersionTable(versionTable),
		dbmigrate.WithEnsureSchema(schemaName),
		dbmigrate.WithSessionLock(),
	)
}

// RequireVersion returns an error unless identity's currently applied
// schema version is at least minVersion, the version the calling binary was
// built against — refusing to let it boot against an older identity schema.
// A newer-than-built schema is explicitly allowed: that is what the
// additive-only discipline documented in the package doc pays for.
func RequireVersion(ctx context.Context, dsn string, minVersion int64) error {
	// Deliberately NOT New(): this is a read-only check, and Runner.Status
	// (goose's Provider.Status) acquires the configured session lock via
	// initialize(ctx, true) — verified against goose v3.27.3's source, where
	// only HasPending/GetVersions take the lock-free initialize(ctx, false)
	// path. Building with New's WithSessionLock would make this boot guard
	// contend with whichever app is currently applying identity migrations,
	// failing after goose's five-minute lock retry with an opaque lock error
	// instead of the version error below. WithEnsureSchema stays: goose
	// cannot create the version table against a schema that does not exist.
	r, err := dbmigrate.New(migrationsFS, "migrations",
		dbmigrate.WithVersionTable(versionTable),
		dbmigrate.WithEnsureSchema(schemaName),
	)
	if err != nil {
		return err
	}

	statuses, err := r.Status(ctx, dsn)
	if err != nil {
		return fmt.Errorf("identity/migrate: check schema version: %w", err)
	}

	var applied int64
	for _, s := range statuses {
		if s.Applied && s.Version > applied {
			applied = s.Version
		}
	}
	if applied < minVersion {
		return fmt.Errorf("identity/migrate: schema version %d is older than the minimum %d this binary requires; run migrations", applied, minVersion)
	}
	return nil
}
