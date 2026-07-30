// Package domain holds the identity bounded context's entities, value
// objects, and ports (interfaces): Household, Member with the unified
// owner/adult/child Role vocabulary, and Credential — the password half of
// a member's login. Nothing here imports either app; the ports are
// implemented by identity/adapter and consumed by identity/app.
//
// household and member ARE the identity domain here, not a reference to
// Nestova's existing internal/household or internal/auth packages, which
// stay app-side. See identity/migrate's package doc for the schema this
// package's adapters run against.
package domain
