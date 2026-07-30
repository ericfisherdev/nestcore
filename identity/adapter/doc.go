// Package adapter contains the identity bounded context's outbound
// adapters: pgx-backed implementations of the ports declared in
// identity/domain, run against the schema identity/migrate owns.
//
// Every query schema-qualifies identity. explicitly rather than relying on
// search_path resolution, because callers connect with their own app's
// search path (see identity/migrate's package doc).
package adapter
