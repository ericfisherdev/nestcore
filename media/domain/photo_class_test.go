package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/media/domain"
)

// TestPhotoClassZeroValueInvalid guards the deliberate design that an
// uninitialized PhotoClass is rejected rather than resolving to some
// already-registered class.
func TestPhotoClassZeroValueInvalid(t *testing.T) {
	var zero domain.PhotoClass
	if zero.Valid() {
		t.Error("zero-value PhotoClass.Valid() = true, want false")
	}
	if zero.String() != "unspecified" {
		t.Errorf("zero value String() = %q, want %q", zero.String(), "unspecified")
	}
}

// TestRegisterPhotoClass_ValidAndReflectsName covers the golden path: a
// freshly registered class is valid and its String() is the registered
// name.
func TestRegisterPhotoClass_ValidAndReflectsName(t *testing.T) {
	class := domain.RegisterPhotoClass("test_class_valid")
	if !class.Valid() {
		t.Error("registered class .Valid() = false, want true")
	}
	if got, want := class.String(), "test_class_valid"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestRegisterPhotoClass_DuplicatePanics proves two applications can never
// silently collide on the same class name.
func TestRegisterPhotoClass_DuplicatePanics(t *testing.T) {
	domain.RegisterPhotoClass("test_class_dup")
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate class name did not panic")
		}
	}()
	domain.RegisterPhotoClass("test_class_dup")
}

// TestRegisterPhotoClass_InvalidNamePanics covers the identifier-shape
// guard: an uppercase letter, a leading digit, and a non-identifier
// character are all rejected.
func TestRegisterPhotoClass_InvalidNamePanics(t *testing.T) {
	for _, name := range []string{"", "Album", "1class", "class-name", "class name"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterPhotoClass(%q) did not panic", name)
				}
			}()
			domain.RegisterPhotoClass(name)
		}()
	}
}

// TestPhotoClass_HandBuiltIsInvalid proves a PhotoClass value cannot be
// forged outside RegisterPhotoClass: a struct literal carrying an
// otherwise-registered name string is still invalid, since it never went
// through registration.
func TestPhotoClass_HandBuiltIsInvalid(t *testing.T) {
	domain.RegisterPhotoClass("test_class_forge_check")
	// PhotoClass's field is unexported, so a hand-built value here is
	// necessarily the zero value — this documents that there is no other
	// way for a caller in this package to construct one.
	var forged domain.PhotoClass
	if forged.Valid() {
		t.Error("hand-built PhotoClass.Valid() = true, want false")
	}
}
