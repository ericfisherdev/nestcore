package adapter

import (
	"time"

	"github.com/xor-gate/goexif2/exif"

	"github.com/ericfisherdev/nestcore/media/domain"
)

// ExifReader is a domain.ExifReader that reads the EXIF capture time from a
// photo's bytes using the xor-gate/goexif2 fork. EXIF parsing runs over
// untrusted upload bytes, so TakenAt is hardened to never crash on
// malformed input.
type ExifReader struct{}

// NewExifReader returns an ExifReader.
func NewExifReader() ExifReader { return ExifReader{} }

var _ domain.ExifReader = ExifReader{}

// TakenAt returns the photo's EXIF capture time normalized to UTC, or nil
// when the image carries no usable EXIF date. A missing tag, an
// undecodable EXIF block, or a panic deep in the parser on malformed input
// is not an error — the photo is simply stored without a taken_at. The
// recover guard ensures a crafted image cannot crash the caller through the
// third-party parser. r must support random access because EXIF/TIFF fields
// are addressed by absolute byte offsets; goexif2 seeks directly to the
// EXIF/TIFF segment it locates rather than buffering the whole input up
// front, though on an image carrying no EXIF block at all it may scan
// sequentially through the entire reader looking for one. Passing a
// domain.PhotoReader straight through (as PhotoService does) still avoids a
// separate in-memory buffering step either way.
func (ExifReader) TakenAt(r domain.RandomAccessReader) (taken *time.Time) {
	defer func() {
		if rec := recover(); rec != nil {
			taken = nil
		}
	}()
	x, err := exif.Decode(r)
	if err != nil {
		return nil
	}
	// DateTime() prefers DateTimeOriginal and falls back to DateTime; it
	// returns the camera/local zone, so normalize to UTC for a consistent,
	// timezone-independent stored value.
	t, err := x.DateTime()
	if err != nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}
