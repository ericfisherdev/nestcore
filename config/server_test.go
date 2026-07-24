package config_test

import (
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadServer(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.ServerConfig
	}{
		{
			name: "defaults when empty",
			env:  map[string]string{},
			want: config.ServerConfig{Addr: ":8080", RequestTimeout: 120 * time.Second},
		},
		{
			name: "colon-prefixed PORT is normalized",
			env:  map[string]string{"PORT": ":3000"},
			want: config.ServerConfig{Addr: ":3000", RequestTimeout: 120 * time.Second},
		},
		{
			name: "bare PORT",
			env:  map[string]string{"PORT": "9090"},
			want: config.ServerConfig{Addr: ":9090", RequestTimeout: 120 * time.Second},
		},
		{
			name: "explicit trusted proxies list is stored raw",
			env:  map[string]string{"TRUSTED_PROXIES": "10.0.0.0/8, 192.168.0.0/16"},
			want: config.ServerConfig{Addr: ":8080", TrustedProxies: "10.0.0.0/8, 192.168.0.0/16", RequestTimeout: 120 * time.Second},
		},
		{
			name: "explicit SERVER_REQUEST_TIMEOUT override",
			env:  map[string]string{"SERVER_REQUEST_TIMEOUT": "45s"},
			want: config.ServerConfig{Addr: ":8080", RequestTimeout: 45 * time.Second},
		},
		{
			name: "PUBLIC_BASE_URL is trimmed of a trailing slash",
			env:  map[string]string{"PUBLIC_BASE_URL": "https://app.tailnet.ts.net/"},
			want: config.ServerConfig{Addr: ":8080", RequestTimeout: 120 * time.Second, PublicBaseURL: "https://app.tailnet.ts.net"},
		},
		{
			// TrimSuffix removes only ONE trailing slash occurrence; TrimRight
			// strips them all, so this must normalize down to a clean origin
			// exactly like the single-trailing-slash case above.
			name: "PUBLIC_BASE_URL with multiple trailing slashes is fully trimmed",
			env:  map[string]string{"PUBLIC_BASE_URL": "https://app.tailnet.ts.net//"},
			want: config.ServerConfig{Addr: ":8080", RequestTimeout: 120 * time.Second, PublicBaseURL: "https://app.tailnet.ts.net"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadServer()
			if len(errs) > 0 {
				t.Fatalf("LoadServer() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadServer() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadServerInvalidRequestTimeout(t *testing.T) {
	setEnv(t, map[string]string{"SERVER_REQUEST_TIMEOUT": "5x"})
	_, errs := config.LoadServer()
	if len(errs) == 0 || !contains(errsToString(errs), "SERVER_REQUEST_TIMEOUT") {
		t.Errorf("LoadServer() errors = %v, want a SERVER_REQUEST_TIMEOUT error", errs)
	}
}

func TestServerConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.ServerConfig
		wantContains []string
	}{
		{
			name: "valid minimal config passes",
			cfg:  config.ServerConfig{RequestTimeout: 120 * time.Second},
		},
		{
			name:         "request timeout below the minimum",
			cfg:          config.ServerConfig{RequestTimeout: 10 * time.Second},
			wantContains: []string{"SERVER_REQUEST_TIMEOUT", "at least"},
		},
		{
			name: "empty trusted proxies passes (trusts nothing)",
			cfg:  config.ServerConfig{RequestTimeout: 120 * time.Second},
		},
		{
			name:         "malformed trusted proxies CIDR",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, TrustedProxies: "127.0.0.0/8, not-a-cidr"},
			wantContains: []string{"TRUSTED_PROXIES", "not-a-cidr"},
		},
		{
			name:         "malformed public base url",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "not-a-url"},
			wantContains: []string{"PUBLIC_BASE_URL", "absolute"},
		},
		{
			name:         "relative public base url is rejected",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "/go/1"},
			wantContains: []string{"PUBLIC_BASE_URL", "absolute"},
		},
		{
			// This branch is unreachable through LoadServer, which always
			// trims a trailing slash before returning ServerConfig — this
			// constructs the struct directly to reach it, as defense in
			// depth for a caller that does not pre-trim.
			name:         "bare trailing slash origin is rejected",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "https://example.org/"},
			wantContains: []string{"PUBLIC_BASE_URL", "origin only"},
		},
		{
			name:         "public base url with a query string is rejected",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "https://example.org?foo=bar"},
			wantContains: []string{"PUBLIC_BASE_URL", "origin only"},
		},
		{
			name:         "public base url with a fragment is rejected",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "https://example.org#section"},
			wantContains: []string{"PUBLIC_BASE_URL", "origin only"},
		},
		{
			name:         "public base url with userinfo is rejected",
			cfg:          config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "https://user:pass@example.org"},
			wantContains: []string{"PUBLIC_BASE_URL", "origin only"},
		},
		{
			name: "clean origin passes",
			cfg:  config.ServerConfig{RequestTimeout: 120 * time.Second, PublicBaseURL: "https://example.org"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.Validate()
			if len(tt.wantContains) == 0 {
				if len(errs) > 0 {
					t.Errorf("Validate() = %v, want no errors", errs)
				}
				return
			}
			joined := errsToString(errs)
			for _, want := range tt.wantContains {
				if !contains(joined, want) {
					t.Errorf("Validate() = %q, want it to contain %q", joined, want)
				}
			}
		})
	}
}

// TestTrustedProxies is split into one top-level function per scenario
// (rather than t.Run subtests sharing one function): the default, the
// explicit-empty override, prefix parsing, and Validate's reporting are
// unrelated concerns, so splitting keeps cognitive complexity low without
// forcing them into an artificial input/expected table.

func TestTrustedProxies_UnsetDefaultsToLoopback(t *testing.T) {
	setEnv(t, nil)
	_ = os.Unsetenv("TRUSTED_PROXIES")
	cfg, errs := config.LoadServer()
	if len(errs) > 0 {
		t.Fatalf("LoadServer() unexpected errors: %v", errs)
	}
	if cfg.TrustedProxies != "127.0.0.0/8,::1/128" {
		t.Errorf("TrustedProxies = %q, want the loopback default", cfg.TrustedProxies)
	}
	if got := cfg.TrustedProxyPrefixes(); len(got) != 2 {
		t.Errorf("TrustedProxyPrefixes() len = %d, want 2", len(got))
	}
}

func TestTrustedProxies_ExplicitEmptyTrustsNothing(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": ""})
	cfg, errs := config.LoadServer()
	if len(errs) > 0 {
		t.Fatalf("LoadServer() unexpected errors: %v", errs)
	}
	if cfg.TrustedProxies != "" {
		t.Errorf("TrustedProxies = %q, want empty", cfg.TrustedProxies)
	}
	if got := cfg.TrustedProxyPrefixes(); len(got) != 0 {
		t.Errorf("TrustedProxyPrefixes() len = %d, want 0 (trust nothing)", len(got))
	}
}

func TestTrustedProxies_PrefixesAreParsedAndMasked(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": "192.168.1.5/24, ::1/128"})
	cfg, errs := config.LoadServer()
	if len(errs) > 0 {
		t.Fatalf("LoadServer() unexpected errors: %v", errs)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"), // host bits masked off
		netip.MustParsePrefix("::1/128"),
	}
	if got := cfg.TrustedProxyPrefixes(); !reflect.DeepEqual(got, want) {
		t.Errorf("TrustedProxyPrefixes() = %v, want %v", got, want)
	}
}

func TestTrustedProxies_MalformedCIDRIsReportedByValidate(t *testing.T) {
	setEnv(t, map[string]string{"TRUSTED_PROXIES": "127.0.0.0/8, not-a-cidr"})
	cfg, errs := config.LoadServer()
	if len(errs) > 0 {
		t.Fatalf("LoadServer() unexpected errors: %v", errs)
	}
	joined := errsToString(cfg.Validate())
	if !contains(joined, "TRUSTED_PROXIES") || !contains(joined, "not-a-cidr") {
		t.Errorf("Validate() = %q, want it to name TRUSTED_PROXIES and the bad entry", joined)
	}
}
