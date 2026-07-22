package config

import (
	"errors"
	"fmt"
	"time"
)

// defaultS3PresignTTL is S3_PRESIGN_TTL's default: long enough for a slow
// connection to actually fetch an object after a redirect, short enough
// that a leaked/cached URL stops working soon after.
const defaultS3PresignTTL = 15 * time.Minute

// S3Config configures an optional S3-compatible object storage backend.
// LoadS3 always parses every field; whether it applies to a given
// deployment is the caller's own decision (e.g. a storage-backend selector
// that only exists in the caller's own domain config), so Validate is
// caller-gated rather than self-gating on an Enabled field the way
// SMSConfig and EmailConfig do — see Validate's own doc.
type S3Config struct {
	// Endpoint is the S3-compatible API's base URL. Blank targets real AWS
	// S3 (the SDK's regional default endpoint); a custom endpoint (MinIO,
	// Garage, Cloudflare R2, ...) is a first-class target, not an
	// afterthought.
	Endpoint string
	// Region is passed to every S3 request. AWS S3 requires a real region;
	// most S3-compatible servers (MinIO, Garage) accept any non-empty value
	// since they do not partition by region.
	Region string
	// Bucket is the bucket objects are stored under.
	Bucket string
	// AccessKeyID / SecretAccessKey are optional static credentials. When
	// BOTH are blank, the AWS SDK's default credential chain (environment,
	// shared config/credentials file, EC2/ECS instance role, etc.) supplies
	// credentials instead, so a deployment that already provisions
	// credentials another way (e.g. an IAM role) never needs to duplicate
	// them here. Validate enforces both-or-neither, mirroring TLSConfig's
	// CertFile/KeyFile pairing.
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle forces path-style bucket addressing
	// (https://endpoint/bucket/key instead of https://bucket.endpoint/key).
	// MinIO and most self-hosted S3-compatible servers require this; real
	// AWS S3 does not.
	UsePathStyle bool
	// PresignTTL is how long a presigned GET URL stays valid when the
	// caller passes a non-positive ttl of its own — the applied default.
	// Kept short: a presigned URL is a bearer credential for as long as it
	// is valid, so the default favors a tight window over convenience.
	PresignTTL time.Duration
}

// LoadS3 reads S3Config from S3_ENDPOINT, S3_REGION, S3_BUCKET,
// S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_USE_PATH_STYLE, and
// S3_PRESIGN_TTL. It always parses every field, regardless of whether the
// caller's deployment actually selected an S3 backend; the caller decides
// whether the returned errors and Validate's findings count — see
// S3Config's own doc.
func LoadS3() (S3Config, []error) {
	var errs []error

	presignTTL, err := Duration("S3_PRESIGN_TTL", defaultS3PresignTTL)
	if err != nil {
		errs = append(errs, err)
	}
	usePathStyle, err := Bool("S3_USE_PATH_STYLE", false)
	if err != nil {
		errs = append(errs, err)
	}

	return S3Config{
		Endpoint:        trimmed("S3_ENDPOINT"),
		Region:          trimmed("S3_REGION"),
		Bucket:          trimmed("S3_BUCKET"),
		AccessKeyID:     trimmed("S3_ACCESS_KEY_ID"),
		SecretAccessKey: trimmed("S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    usePathStyle,
		PresignTTL:      presignTTL,
	}, errs
}

// Validate returns every S3Config problem found, so callers can surface
// them together. Unlike SMSConfig.Validate and EmailConfig.Validate, which
// self-gate on their own Enabled field, Validate here is caller-gated: it
// always checks the required fields, and it is the caller's responsibility
// to call it (and to append LoadS3's own errors) only when its own
// storage-backend selection actually opted into S3.
func (s S3Config) Validate() []error {
	var errs []error

	if s.Bucket == "" {
		errs = append(errs, errors.New("S3_BUCKET is required when the S3 backend is selected"))
	}
	if s.Region == "" {
		errs = append(errs, errors.New("S3_REGION is required when the S3 backend is selected"))
	}
	if s.PresignTTL <= 0 {
		errs = append(errs, fmt.Errorf("S3_PRESIGN_TTL must be positive, got %v", s.PresignTTL))
	}
	// Static credentials are both-or-neither: a lone access key or secret
	// is always a misconfiguration, never a valid partial state.
	if (s.AccessKeyID == "") != (s.SecretAccessKey == "") {
		errs = append(errs, errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together (or both left unset to use the default AWS credential chain)"))
	}

	return errs
}
