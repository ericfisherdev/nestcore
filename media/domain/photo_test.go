package domain_test

import (
	"errors"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/media/domain"
)

// validSha256Hex is a syntactically valid (64-character lowercase hex)
// content hash for building a well-formed Photo in tests; its value is
// otherwise arbitrary.
const validSha256Hex = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestPhotoValidate(t *testing.T) {
	ok := domain.Photo{
		ID:          domain.NewPhotoID(),
		HouseholdID: identity.NewHouseholdID(),
		StorageRef:  domain.StorageRef("hh/ab/abc123.jpg"),
		ContentHash: validSha256Hex,
		SizeBytes:   1024,
		ContentType: domain.ContentTypeJPEG,
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid photo rejected: %v", err)
	}
	for _, ref := range []domain.StorageRef{"", "   "} {
		p := ok
		p.StorageRef = ref
		if !errors.Is(p.Validate(), domain.ErrInvalidPhoto) {
			t.Fatalf("blank storage ref %q accepted", ref)
		}
	}

	invalidHashes := []string{
		"",
		"   ",
		"deadbeef",                      // too short
		strings.Repeat("a", 65),         // too long
		strings.ToUpper(validSha256Hex), // must be lowercase
		"g" + validSha256Hex[1:],        // 'g' is not hex
	}
	for _, hash := range invalidHashes {
		p := ok
		p.ContentHash = hash
		if !errors.Is(p.Validate(), domain.ErrInvalidPhoto) {
			t.Fatalf("invalid content hash %q accepted", hash)
		}
	}

	for _, size := range []int64{0, -1} {
		p := ok
		p.SizeBytes = size
		if !errors.Is(p.Validate(), domain.ErrInvalidPhoto) {
			t.Fatalf("non-positive size bytes %d accepted", size)
		}
	}

	for _, ct := range []string{"", "application/pdf", "image/heic"} {
		p := ok
		p.ContentType = ct
		if !errors.Is(p.Validate(), domain.ErrInvalidPhoto) {
			t.Fatalf("unaccepted content type %q accepted", ct)
		}
	}

	// Every entry in the accept-list is itself valid.
	for _, ct := range []string{domain.ContentTypeJPEG, domain.ContentTypePNG, domain.ContentTypeWebP} {
		p := ok
		p.ContentType = ct
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted content type %q rejected: %v", ct, err)
		}
	}
}

func TestValidContentHash(t *testing.T) {
	if !domain.ValidContentHash(validSha256Hex) {
		t.Errorf("ValidContentHash(%q) = false, want true", validSha256Hex)
	}
	for _, hash := range []string{"", "not-a-hash", strings.ToUpper(validSha256Hex), validSha256Hex[:63]} {
		if domain.ValidContentHash(hash) {
			t.Errorf("ValidContentHash(%q) = true, want false", hash)
		}
	}
}
