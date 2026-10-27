package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

// runSecurity chains ForwardedHeaders (so IsHTTPS is resolved exactly as in the
// real server) ahead of SecurityHeaders and returns the response headers.
func runSecurity(t *testing.T, hsts string, setup func(*http.Request)) http.Header {
	t.Helper()
	return runWithMiddleware(t, middleware.SecurityHeaders(hsts), setup)
}

// runWithMiddleware chains ForwardedHeaders ahead of sec and returns the
// response headers of a request served through them.
func runWithMiddleware(t *testing.T, sec middleware.Middleware, setup func(*http.Request)) http.Header {
	t.Helper()
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	h := middleware.ForwardedHeaders(trusted)(
		sec(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	if setup != nil {
		setup(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Header
}

func TestSecurityHeaders(t *testing.T) {
	t.Run("baseline headers on every response", func(t *testing.T) {
		hdr := runSecurity(t, "", nil)
		want := map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "strict-origin-when-cross-origin",
			"Content-Security-Policy": "frame-ancestors 'self'",
			"X-Frame-Options":         "SAMEORIGIN",
		}
		for k, v := range want {
			if got := hdr.Get(k); got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
	})

	t.Run("HSTS emitted over https when enabled", func(t *testing.T) {
		const value = "max-age=15552000; includeSubDomains"
		hdr := runSecurity(t, value, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", "https")
		})
		if got := hdr.Get("Strict-Transport-Security"); got != value {
			t.Errorf("Strict-Transport-Security = %q, want %q", got, value)
		}
	})

	t.Run("HSTS absent over http", func(t *testing.T) {
		hdr := runSecurity(t, "max-age=15552000", nil) // no XFP -> effective http
		if got := hdr.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q, want empty over http", got)
		}
	})

	t.Run("HSTS absent when disabled even over https", func(t *testing.T) {
		hdr := runSecurity(t, "", func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", "https")
		})
		if got := hdr.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q, want empty when disabled", got)
		}
	})

	t.Run("no duplicate X-Content-Type-Options", func(t *testing.T) {
		hdr := runSecurity(t, "", nil)
		if vals := hdr.Values("X-Content-Type-Options"); len(vals) != 1 {
			t.Errorf("X-Content-Type-Options has %d values, want 1", len(vals))
		}
	})
}

func TestSecurityHeadersWithPolicy(t *testing.T) {
	const hsts = "max-age=15552000; includeSubDomains"
	strict := middleware.ContentSecurityPolicy{
		{Name: "default-src", Values: []string{"'self'"}},
		{Name: "script-src", Values: []string{"'self'"}},
		{Name: "object-src", Values: []string{"'none'"}},
		{Name: "base-uri", Values: []string{"'none'"}},
		{Name: "frame-ancestors", Values: []string{"'self'"}},
	}
	tests := []struct {
		name    string
		policy  middleware.ContentSecurityPolicy
		wantCSP string
		wantSet bool
	}{
		{
			name:    "custom multi-directive policy is emitted verbatim",
			policy:  strict,
			wantCSP: "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'",
			wantSet: true,
		},
		{name: "nil policy omits the header", policy: nil},
		{name: "empty policy omits the header", policy: middleware.ContentSecurityPolicy{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := runWithMiddleware(t, middleware.SecurityHeadersWithPolicy(hsts, tt.policy), func(r *http.Request) {
				r.Header.Set("X-Forwarded-Proto", "https")
			})
			vals := hdr.Values("Content-Security-Policy")
			if tt.wantSet {
				if len(vals) != 1 || vals[0] != tt.wantCSP {
					t.Errorf("Content-Security-Policy = %q, want exactly [%q]", vals, tt.wantCSP)
				}
			} else if len(vals) != 0 {
				t.Errorf("Content-Security-Policy = %q, want absent", vals)
			}
			want := map[string]string{
				"X-Content-Type-Options":    "nosniff",
				"Referrer-Policy":           "strict-origin-when-cross-origin",
				"X-Frame-Options":           "SAMEORIGIN",
				"Strict-Transport-Security": hsts,
			}
			for k, v := range want {
				if got := hdr.Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}

	t.Run("HSTS stays absent over http", func(t *testing.T) {
		hdr := runWithMiddleware(t, middleware.SecurityHeadersWithPolicy(hsts, strict), nil)
		if got := hdr.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q, want empty over http", got)
		}
	})
}
