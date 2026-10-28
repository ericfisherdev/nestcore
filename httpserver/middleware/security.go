package middleware

import "net/http"

// SecurityHeaders sets baseline security response headers on every response, and
// HSTS only over an effective-HTTPS request. The headers are applied before the
// handler runs so they survive on every status code. It emits the historical
// DefaultCSP policy; use SecurityHeadersWithPolicy to supply a stricter
// Content-Security-Policy.
//
// hsts is the precomputed Strict-Transport-Security value (e.g.
// "max-age=15552000; includeSubDomains") or "" when HSTS is disabled; the header
// is emitted only when hsts is non-empty AND the effective scheme is https
// (IsHTTPS, set by ForwardedHeaders), so it is never sent over the plain-HTTP
// internal hop behind a TLS-terminating proxy.
func SecurityHeaders(hsts string) Middleware {
	return SecurityHeadersWithPolicy(hsts, DefaultCSP())
}

// SecurityHeadersWithPolicy is SecurityHeaders with a caller-supplied
// Content-Security-Policy. The policy is rendered once, when the middleware is
// built, and emitted verbatim on every response; a nil or empty csp renders ""
// and the Content-Security-Policy header is then omitted entirely. The other
// headers and the HSTS gating are identical to SecurityHeaders.
func SecurityHeadersWithPolicy(hsts string, csp ContentSecurityPolicy) Middleware {
	policy := csp.String()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// Prevent MIME sniffing globally (not just for static assets).
			h.Set("X-Content-Type-Options", "nosniff")
			// Trim the Referer on cross-origin navigations.
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			// Framing protection plus whatever else the caller's policy
			// restricts. See ContentSecurityPolicy.
			if policy != "" {
				h.Set("Content-Security-Policy", policy)
			}
			h.Set("X-Frame-Options", "SAMEORIGIN")
			if hsts != "" && IsHTTPS(r.Context()) {
				h.Set("Strict-Transport-Security", hsts)
			}
			next.ServeHTTP(w, r)
		})
	}
}
