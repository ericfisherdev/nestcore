package middleware

import "strings"

// CSPDirective is one Content-Security-Policy directive: a name such as
// "script-src" and the source expressions that follow it, such as "'self'".
type CSPDirective struct {
	Name   string
	Values []string
}

// String renders the directive as its name followed by its space-separated
// values; a directive without values renders as the bare name.
func (d CSPDirective) String() string {
	return strings.Join(append([]string{d.Name}, d.Values...), " ")
}

// ContentSecurityPolicy is an ordered list of Content-Security-Policy
// directives. Order is the caller's and is preserved when rendering, so the
// emitted header is deterministic and a test can assert it byte-for-byte. A
// policy is a compile-time constant of the consuming application; it carries
// no per-request state such as a nonce.
type ContentSecurityPolicy []CSPDirective

// String renders the policy as a Content-Security-Policy header value:
// directives in slice order, joined by "; ", each as its name followed by its
// space-separated values. A nil or empty policy renders "".
func (p ContentSecurityPolicy) String() string {
	directives := make([]string, len(p))
	for i, d := range p {
		directives[i] = d.String()
	}
	return strings.Join(directives, "; ")
}

// DefaultCSP returns a fresh copy of the historical default policy: it only
// restricts framing to the page's own origin and leaves every fetch directive
// open. SecurityHeaders and httpserver.New use it when no policy is supplied.
// Each call allocates new slices, so mutating one result never changes the
// framing policy another caller receives.
func DefaultCSP() ContentSecurityPolicy {
	return ContentSecurityPolicy{
		{Name: "frame-ancestors", Values: []string{"'self'"}},
	}
}

// FrameAncestorsSelf is a package-level value of DefaultCSP for callers that
// want to reference or extend the default. SecurityHeaders and httpserver.New
// do not read it, so mutating its slices cannot weaken their framing policy.
var FrameAncestorsSelf = DefaultCSP()
