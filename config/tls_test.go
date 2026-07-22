package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadTLS(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.TLSConfig
	}{
		{name: "unset is empty", env: map[string]string{}, want: config.TLSConfig{}},
		{
			name: "both files set",
			env:  map[string]string{"TLS_CERT_FILE": "/etc/tls/cert.pem", "TLS_KEY_FILE": "/etc/tls/key.pem"},
			want: config.TLSConfig{CertFile: "/etc/tls/cert.pem", KeyFile: "/etc/tls/key.pem"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			if got := config.LoadTLS(); got != tt.want {
				t.Errorf("LoadTLS() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestTLSConfigEnabled covers the listener-selection decision: TLS is
// enabled only when both files are present.
func TestTLSConfigEnabled(t *testing.T) {
	cases := []struct {
		name string
		tls  config.TLSConfig
		want bool
	}{
		{"both set", config.TLSConfig{CertFile: "c.pem", KeyFile: "k.pem"}, true},
		{"neither set", config.TLSConfig{}, false},
		{"cert only", config.TLSConfig{CertFile: "c.pem"}, false},
		{"key only", config.TLSConfig{KeyFile: "k.pem"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tls.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTLSConfigValidate(t *testing.T) {
	tests := []struct {
		name         string
		tls          config.TLSConfig
		wantContains []string
	}{
		{name: "neither set passes", tls: config.TLSConfig{}},
		{name: "both set passes", tls: config.TLSConfig{CertFile: "c.pem", KeyFile: "k.pem"}},
		{
			name:         "cert without key",
			tls:          config.TLSConfig{CertFile: "c.pem"},
			wantContains: []string{"TLS_CERT_FILE", "TLS_KEY_FILE"},
		},
		{
			name:         "key without cert",
			tls:          config.TLSConfig{KeyFile: "k.pem"},
			wantContains: []string{"TLS_CERT_FILE", "TLS_KEY_FILE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.tls.Validate()
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
