package config_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestLoadCache(t *testing.T) {
	t.Run("defaults when empty", func(t *testing.T) {
		setEnv(t, map[string]string{})
		got := config.LoadCache()
		if got.Dir != "./.localdata/cache" {
			t.Errorf("LoadCache().Dir = %q, want the dev default", got.Dir)
		}
	})
	t.Run("explicit dir override", func(t *testing.T) {
		setEnv(t, map[string]string{"CACHE_DIR": "/var/lib/app/cache"})
		got := config.LoadCache()
		if got.Dir != "/var/lib/app/cache" {
			t.Errorf("LoadCache().Dir = %q, want /var/lib/app/cache", got.Dir)
		}
	})
}

func TestCacheConfigValidate(t *testing.T) {
	if errs := (config.CacheConfig{Dir: "./cache"}).Validate(); len(errs) > 0 {
		t.Errorf("Validate() = %v, want no errors", errs)
	}
	errs := (config.CacheConfig{}).Validate()
	if len(errs) == 0 || !contains(errsToString(errs), "CACHE_DIR") {
		t.Errorf("Validate() = %v, want a CACHE_DIR error", errs)
	}
}
