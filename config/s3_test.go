package config_test

import (
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadS3(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.S3Config
	}{
		{
			name: "defaults when empty",
			env:  map[string]string{},
			want: config.S3Config{PresignTTL: 15 * time.Minute},
		},
		{
			name: "custom endpoint and static credentials",
			env: map[string]string{
				"S3_ENDPOINT": "http://127.0.0.1:9000", "S3_REGION": "us-east-1", "S3_BUCKET": "photos",
				"S3_ACCESS_KEY_ID": "minioadmin", "S3_SECRET_ACCESS_KEY": "minioadmin",
				"S3_USE_PATH_STYLE": "true", "S3_PRESIGN_TTL": "5m",
			},
			want: config.S3Config{
				Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "photos",
				AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin", UsePathStyle: true,
				PresignTTL: 5 * time.Minute,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			got, errs := config.LoadS3()
			if len(errs) > 0 {
				t.Fatalf("LoadS3() unexpected errors: %v", errs)
			}
			if got != tt.want {
				t.Errorf("LoadS3() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadS3AlwaysParses covers a deliberate behavior change from the
// monolithic config this package was extracted from: since S3Config no
// longer knows which caller-owned setting gates it, LoadS3 always attempts
// to parse every field — it is the caller's job to decide, based on its own
// gate, whether these errors (and Validate's findings) count. A caller with
// S3 disabled discards them, the same way LoadS3's fallback values still
// leave S3Config in a sane state even when the raw environment is
// malformed.
func TestLoadS3AlwaysParses(t *testing.T) {
	setEnv(t, map[string]string{"S3_PRESIGN_TTL": "not-a-duration", "S3_USE_PATH_STYLE": "not-a-bool"})
	_, errs := config.LoadS3()
	if len(errs) != 2 {
		t.Fatalf("LoadS3() errors = %v, want 2 parse errors", errs)
	}
}

func TestS3ConfigValidate(t *testing.T) {
	valid := config.S3Config{Bucket: "photos", Region: "us-east-1", PresignTTL: 15 * time.Minute}

	tests := []struct {
		name         string
		mutate       func(config.S3Config) config.S3Config
		wantContains []string
	}{
		{name: "valid config passes", mutate: func(c config.S3Config) config.S3Config { return c }},
		{
			name:         "missing bucket and region",
			mutate:       func(c config.S3Config) config.S3Config { c.Bucket, c.Region = "", ""; return c },
			wantContains: []string{"S3_BUCKET", "S3_REGION"},
		},
		{
			name:         "non-positive presign ttl",
			mutate:       func(c config.S3Config) config.S3Config { c.PresignTTL = 0; return c },
			wantContains: []string{"S3_PRESIGN_TTL", "positive"},
		},
		{
			name:         "access key without secret",
			mutate:       func(c config.S3Config) config.S3Config { c.AccessKeyID = "minioadmin"; return c },
			wantContains: []string{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"},
		},
		{
			name:         "secret without access key",
			mutate:       func(c config.S3Config) config.S3Config { c.SecretAccessKey = "minioadmin"; return c },
			wantContains: []string{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"},
		},
		{
			name: "both credentials set together passes",
			mutate: func(c config.S3Config) config.S3Config {
				c.AccessKeyID, c.SecretAccessKey = "minioadmin", "minioadmin"
				return c
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.mutate(valid).Validate()
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
