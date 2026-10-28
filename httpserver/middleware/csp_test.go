package middleware_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

func TestContentSecurityPolicyString(t *testing.T) {
	tests := []struct {
		name   string
		policy middleware.ContentSecurityPolicy
		want   string
	}{
		{name: "nil policy", policy: nil, want: ""},
		{name: "empty policy", policy: middleware.ContentSecurityPolicy{}, want: ""},
		{
			name: "single directive single value",
			policy: middleware.ContentSecurityPolicy{
				{Name: "object-src", Values: []string{"'none'"}},
			},
			want: "object-src 'none'",
		},
		{
			name: "multi-value directive",
			policy: middleware.ContentSecurityPolicy{
				{Name: "script-src", Values: []string{"'self'", "https://cdn.example"}},
			},
			want: "script-src 'self' https://cdn.example",
		},
		{
			name: "directive without values renders its bare name",
			policy: middleware.ContentSecurityPolicy{
				{Name: "upgrade-insecure-requests"},
			},
			want: "upgrade-insecure-requests",
		},
		{
			name: "directives keep slice order",
			policy: middleware.ContentSecurityPolicy{
				{Name: "style-src", Values: []string{"'self'"}},
				{Name: "base-uri", Values: []string{"'none'"}},
				{Name: "script-src", Values: []string{"'self'"}},
			},
			want: "style-src 'self'; base-uri 'none'; script-src 'self'",
		},
		{name: "frame-ancestors default", policy: middleware.FrameAncestorsSelf, want: "frame-ancestors 'self'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultCSP_ReturnsIndependentCopies(t *testing.T) {
	first := middleware.DefaultCSP()
	first[0].Values[0] = "'none'"

	if got := middleware.DefaultCSP().String(); got != "frame-ancestors 'self'" {
		t.Errorf("DefaultCSP() after mutating an earlier result = %q, want %q", got, "frame-ancestors 'self'")
	}
}

func TestSecurityHeaders_IgnoresMutationOfExportedDefault(t *testing.T) {
	original := middleware.FrameAncestorsSelf[0].Values[0]
	middleware.FrameAncestorsSelf[0].Values[0] = "*"
	t.Cleanup(func() { middleware.FrameAncestorsSelf[0].Values[0] = original })

	got := runSecurity(t, "", nil).Get("Content-Security-Policy")
	if got != "frame-ancestors 'self'" {
		t.Errorf("Content-Security-Policy = %q, want %q", got, "frame-ancestors 'self'")
	}
}
