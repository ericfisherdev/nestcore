package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/media/domain"
)

func TestPhotoIDRoundTrip(t *testing.T) {
	id := domain.NewPhotoID()
	got, err := domain.ParsePhotoID(id.String())
	if err != nil {
		t.Fatalf("ParsePhotoID: %v", err)
	}
	if got != id {
		t.Fatalf("round-trip = %s, want %s", got, id)
	}
	if _, err := domain.ParsePhotoID("nope"); err == nil {
		t.Fatal("ParsePhotoID(invalid) = nil error, want error")
	}
}
