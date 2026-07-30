package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadSchemas(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want config.SchemaConfig
	}{
		{
			name: "defaults when empty",
			env:  map[string]string{},
			want: config.SchemaConfig{Identity: "identity", Nestova: "nestova", Nestorage: "nestorage"},
		},
		{
			name: "overrides are read verbatim",
			env: map[string]string{
				"DB_SCHEMA_IDENTITY": "shared_identity", "DB_SCHEMA_NESTOVA": "nestova2", "DB_SCHEMA_NESTORAGE": "nestorage2",
			},
			want: config.SchemaConfig{Identity: "shared_identity", Nestova: "nestova2", Nestorage: "nestorage2"},
		},
		{
			name: "partial override keeps the other two defaults",
			env:  map[string]string{"DB_SCHEMA_IDENTITY": "custom_identity"},
			want: config.SchemaConfig{Identity: "custom_identity", Nestova: "nestova", Nestorage: "nestorage"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			if got := config.LoadSchemas(); got != tt.want {
				t.Errorf("LoadSchemas() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSchemaConfigValidate(t *testing.T) {
	valid := config.SchemaConfig{Identity: "identity", Nestova: "nestova", Nestorage: "nestorage"}

	tests := []struct {
		name         string
		mutate       func(config.SchemaConfig) config.SchemaConfig
		wantContains []string
	}{
		{
			name:   "valid config passes",
			mutate: func(s config.SchemaConfig) config.SchemaConfig { return s },
		},
		{
			name: "uppercase identity is rejected",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Identity = "Identity"
				return s
			},
			wantContains: []string{"DB_SCHEMA_IDENTITY", "lowercase"},
		},
		{
			name: "hyphen in nestova is rejected",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Nestova = "nest-ova"
				return s
			},
			wantContains: []string{"DB_SCHEMA_NESTOVA"},
		},
		{
			name: "leading digit in nestorage is rejected",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Nestorage = "1nestorage"
				return s
			},
			wantContains: []string{"DB_SCHEMA_NESTORAGE"},
		},
		{
			name: "name over 63 bytes is rejected",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Identity = "a_very_long_schema_name_that_goes_past_the_postgres_identifier_limit"
				return s
			},
			wantContains: []string{"DB_SCHEMA_IDENTITY", "63 bytes"},
		},
		{
			name: "identity and nestova collide",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Nestova = s.Identity
				return s
			},
			wantContains: []string{"DB_SCHEMA_IDENTITY", "DB_SCHEMA_NESTOVA"},
		},
		{
			name: "identity and nestorage collide",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Nestorage = s.Identity
				return s
			},
			wantContains: []string{"DB_SCHEMA_IDENTITY", "DB_SCHEMA_NESTORAGE"},
		},
		{
			name: "nestova and nestorage collide",
			mutate: func(s config.SchemaConfig) config.SchemaConfig {
				s.Nestorage = s.Nestova
				return s
			},
			wantContains: []string{"DB_SCHEMA_NESTOVA", "DB_SCHEMA_NESTORAGE"},
		},
		{
			name: "every problem is reported together",
			mutate: func(_ config.SchemaConfig) config.SchemaConfig {
				return config.SchemaConfig{Identity: "Bad", Nestova: "Bad", Nestorage: "nestorage"}
			},
			wantContains: []string{"DB_SCHEMA_IDENTITY", "DB_SCHEMA_NESTOVA", "DB_SCHEMA_IDENTITY and DB_SCHEMA_NESTOVA"},
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
