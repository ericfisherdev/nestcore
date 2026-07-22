package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/config"
)

func TestString(t *testing.T) {
	t.Run("unset returns fallback", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_STR", "")
		if got := config.String("NESTCORE_TEST_STR", "fallback"); got != "fallback" {
			t.Errorf("String() = %q, want fallback", got)
		}
	})
	t.Run("set returns value", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_STR", "value")
		if got := config.String("NESTCORE_TEST_STR", "fallback"); got != "value" {
			t.Errorf("String() = %q, want value", got)
		}
	})
}

func TestInt32(t *testing.T) {
	t.Run("unset returns fallback with no error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I32", "")
		got, err := config.Int32("NESTCORE_TEST_I32", 7)
		if err != nil || got != 7 {
			t.Errorf("Int32() = (%d, %v), want (7, nil)", got, err)
		}
	})
	t.Run("valid value parses", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I32", "42")
		got, err := config.Int32("NESTCORE_TEST_I32", 7)
		if err != nil || got != 42 {
			t.Errorf("Int32() = (%d, %v), want (42, nil)", got, err)
		}
	})
	t.Run("malformed value returns fallback and a named error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I32", "abc")
		got, err := config.Int32("NESTCORE_TEST_I32", 7)
		if got != 7 {
			t.Errorf("Int32() value = %d, want fallback 7", got)
		}
		if err == nil || !strings.Contains(err.Error(), "NESTCORE_TEST_I32") {
			t.Errorf("Int32() error = %v, want it to name the variable", err)
		}
	})
}

func TestInt64(t *testing.T) {
	t.Run("unset returns fallback with no error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I64", "")
		got, err := config.Int64("NESTCORE_TEST_I64", 7)
		if err != nil || got != 7 {
			t.Errorf("Int64() = (%d, %v), want (7, nil)", got, err)
		}
	})
	t.Run("valid value parses", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I64", "42")
		got, err := config.Int64("NESTCORE_TEST_I64", 7)
		if err != nil || got != 42 {
			t.Errorf("Int64() = (%d, %v), want (42, nil)", got, err)
		}
	})
	t.Run("malformed value returns fallback and a named error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_I64", "not-a-number")
		got, err := config.Int64("NESTCORE_TEST_I64", 7)
		if got != 7 {
			t.Errorf("Int64() value = %d, want fallback 7", got)
		}
		if err == nil || !strings.Contains(err.Error(), "NESTCORE_TEST_I64") {
			t.Errorf("Int64() error = %v, want it to name the variable", err)
		}
	})
}

func TestDuration(t *testing.T) {
	t.Run("unset returns fallback with no error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_DUR", "")
		got, err := config.Duration("NESTCORE_TEST_DUR", 5*time.Second)
		if err != nil || got != 5*time.Second {
			t.Errorf("Duration() = (%v, %v), want (5s, nil)", got, err)
		}
	})
	t.Run("valid value parses", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_DUR", "30s")
		got, err := config.Duration("NESTCORE_TEST_DUR", 5*time.Second)
		if err != nil || got != 30*time.Second {
			t.Errorf("Duration() = (%v, %v), want (30s, nil)", got, err)
		}
	})
	t.Run("malformed value returns fallback and a named error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_DUR", "5x")
		got, err := config.Duration("NESTCORE_TEST_DUR", 5*time.Second)
		if got != 5*time.Second {
			t.Errorf("Duration() value = %v, want fallback 5s", got)
		}
		if err == nil || !strings.Contains(err.Error(), "NESTCORE_TEST_DUR") {
			t.Errorf("Duration() error = %v, want it to name the variable", err)
		}
	})
}

func TestBool(t *testing.T) {
	t.Run("unset returns fallback with no error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_BOOL", "")
		got, err := config.Bool("NESTCORE_TEST_BOOL", true)
		if err != nil || got != true {
			t.Errorf("Bool() = (%v, %v), want (true, nil)", got, err)
		}
	})
	t.Run("valid value parses", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_BOOL", "false")
		got, err := config.Bool("NESTCORE_TEST_BOOL", true)
		if err != nil || got != false {
			t.Errorf("Bool() = (%v, %v), want (false, nil)", got, err)
		}
	})
	t.Run("malformed value returns fallback and a named error", func(t *testing.T) {
		t.Setenv("NESTCORE_TEST_BOOL", "maybe")
		got, err := config.Bool("NESTCORE_TEST_BOOL", true)
		if got != true {
			t.Errorf("Bool() value = %v, want fallback true", got)
		}
		if err == nil || !strings.Contains(err.Error(), "NESTCORE_TEST_BOOL") {
			t.Errorf("Bool() error = %v, want it to name the variable", err)
		}
	})
}
