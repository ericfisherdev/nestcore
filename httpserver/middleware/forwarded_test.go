package middleware_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

// loopback mirrors the TRUSTED_PROXIES default (same-host proxy over loopback).
var loopback = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
}

// captureForwarded runs ForwardedHeaders(trusted) over a request shaped by setup
// and returns the resolved scheme, client IP, and isHTTPS read from the context.
func captureForwarded(t *testing.T, trusted []netip.Prefix, setup func(*http.Request)) (scheme, clientIP string, isHTTPS bool) {
	t.Helper()
	h := middleware.ForwardedHeaders(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		scheme = middleware.RequestScheme(ctx)
		clientIP = middleware.ClientIP(ctx)
		isHTTPS = middleware.IsHTTPS(ctx)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	if setup != nil {
		setup(req)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return scheme, clientIP, isHTTPS
}

// TestForwardedHeaders' scenarios are split into one top-level function each
// (rather than t.Run subtests sharing one function): scheme trust, spoof
// resistance, XFF address selection, and IP normalization are unrelated
// concerns, so splitting keeps cognitive complexity low without forcing
// them into an artificial input/expected table.

func TestForwardedHeaders_TrustedPeerHonorsXForwardedProto(t *testing.T) {
	scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if scheme != "https" || !isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want https/true", scheme, isHTTPS)
	}
}

func TestForwardedHeaders_UntrustedPeerCannotSpoofHTTPS(t *testing.T) {
	scheme, clientIP, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.RemoteAddr = "203.0.113.5:4000" // not loopback
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "10.1.2.3")
	})
	if scheme != "http" || isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want http/false (spoof ignored)", scheme, isHTTPS)
	}
	if clientIP != "203.0.113.5" {
		t.Errorf("clientIP=%q, want the direct peer 203.0.113.5", clientIP)
	}
}

func TestForwardedHeaders_EmptyTrustedListDisablesForwardedTrust(t *testing.T) {
	scheme, clientIP, _ := captureForwarded(t, nil, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "10.1.2.3")
	})
	if scheme != "http" {
		t.Errorf("scheme=%q, want http (no trusted proxies)", scheme)
	}
	if clientIP != "127.0.0.1" {
		t.Errorf("clientIP=%q, want the direct peer 127.0.0.1", clientIP)
	}
}

func TestForwardedHeaders_ClientIPIsRightmostNonTrustedXFF(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	// client -> edge proxy (203.0.113.9) -> internal proxy (10.0.0.1) -> app.
	// 10.0.0.1 is one of ours (trusted), so the real client is 203.0.113.9.
	_, clientIP, _ := captureForwarded(t, trusted, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	})
	if clientIP != "203.0.113.9" {
		t.Errorf("clientIP=%q, want 203.0.113.9 (rightmost non-trusted)", clientIP)
	}
}

func TestForwardedHeaders_AllXFFTrustedFallsBackToPeer(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	// Every hop is one of ours, so there is no client address to extract.
	_, clientIP, _ := captureForwarded(t, trusted, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "10.0.0.5, 127.0.0.2")
	})
	if clientIP != "127.0.0.1" {
		t.Errorf("clientIP=%q, want 127.0.0.1 (direct peer when all XFF trusted)", clientIP)
	}
}

func TestForwardedHeaders_IPv4MappedIPv6PeerIsTrusted(t *testing.T) {
	scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.RemoteAddr = "[::ffff:127.0.0.1]:5000"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if scheme != "https" || !isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want https/true for an IPv4-mapped loopback peer", scheme, isHTTPS)
	}
}

func TestForwardedHeaders_UppercaseProtoIsNormalized(t *testing.T) {
	scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "HTTPS")
	})
	if scheme != "https" || !isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want https/true (uppercase normalized)", scheme, isHTTPS)
	}
}

func TestForwardedHeaders_IPv4MappedIPv6InXFFIsUnmapped(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	_, clientIP, _ := captureForwarded(t, trusted, func(r *http.Request) {
		// A proxy may report the client in IPv4-mapped IPv6 notation.
		r.Header.Set("X-Forwarded-For", "::ffff:203.0.113.9, 10.0.0.1")
	})
	if clientIP != "203.0.113.9" {
		t.Errorf("clientIP=%q, want 203.0.113.9 (unmapped from ::ffff:)", clientIP)
	}
}

func TestForwardedHeaders_XFFAcrossMultipleHeaderLines(t *testing.T) {
	_, clientIP, _ := captureForwarded(t, loopback, func(r *http.Request) {
		r.Header.Add("X-Forwarded-For", "203.0.113.9")
		r.Header.Add("X-Forwarded-For", "127.0.0.2")
	})
	if clientIP != "203.0.113.9" {
		t.Errorf("clientIP=%q, want 203.0.113.9", clientIP)
	}
}

func TestForwardedHeaders_ProtoUsesFirstToken(t *testing.T) {
	scheme, _, _ := captureForwarded(t, loopback, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https, http")
	})
	if scheme != "https" {
		t.Errorf("scheme=%q, want https (first token)", scheme)
	}
}

func TestForwardedHeaders_MalformedProtoFallsBackToOnWireScheme(t *testing.T) {
	// A trusted proxy that is misconfigured (or compromised) must not be able
	// to set an arbitrary scheme; only http/https are honored.
	for _, proto := range []string{"", "ftp", "HTTPS\r\nX-Evil: injected"} {
		scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", proto)
		})
		if scheme != "http" || isHTTPS {
			t.Errorf("proto=%q: scheme=%q isHTTPS=%v, want http/false (fallback)", proto, scheme, isHTTPS)
		}
	}
}

func TestForwardedHeaders_IPv6LoopbackPeerIsTrusted(t *testing.T) {
	scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.RemoteAddr = "[::1]:5000"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if scheme != "https" || !isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want https/true for ::1 peer", scheme, isHTTPS)
	}
}

func TestForwardedHeaders_OnWireTLSYieldsHTTPSWithoutHeader(t *testing.T) {
	// Direct TLS termination (NES-54): r.TLS set even from an untrusted peer.
	scheme, _, isHTTPS := captureForwarded(t, loopback, func(r *http.Request) {
		r.RemoteAddr = "203.0.113.5:4000"
		r.TLS = &tls.ConnectionState{}
	})
	if scheme != "https" || !isHTTPS {
		t.Errorf("scheme=%q isHTTPS=%v, want https/true from r.TLS", scheme, isHTTPS)
	}
}

func TestForwardedHeaders_NoForwardedHeadersFallsBackToPeer(t *testing.T) {
	scheme, clientIP, _ := captureForwarded(t, loopback, nil)
	if scheme != "http" {
		t.Errorf("scheme=%q, want http", scheme)
	}
	if clientIP != "127.0.0.1" {
		t.Errorf("clientIP=%q, want 127.0.0.1", clientIP)
	}
}

// TestForwardedAccessorsWithoutMiddleware documents the zero values seen when
// ForwardedHeaders did not run (e.g. a handler reached outside the chain).
func TestForwardedAccessorsWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if s := middleware.RequestScheme(req.Context()); s != "" {
		t.Errorf("RequestScheme=%q, want empty", s)
	}
	if ip := middleware.ClientIP(req.Context()); ip != "" {
		t.Errorf("ClientIP=%q, want empty", ip)
	}
	if middleware.IsHTTPS(req.Context()) {
		t.Error("IsHTTPS=true, want false")
	}
}
