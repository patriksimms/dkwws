// Package store wraps the small set of S3 operations this tool depends on:
// PutObject with conditional creation, GetObject and HeadObject, all signed
// with AWS Signature Version 4.
//
// Nothing here uses provider-specific administration APIs, so any backend that
// implements those three operations plus If-None-Match can host dkwws.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/link"
)

// responseHeaderTimeout bounds how long the backend may take to start
// answering. Without it a hung endpoint leaves the CLI blocked indefinitely.
const responseHeaderTimeout = 30 * time.Second

// Sentinel errors returned by the store. Callers distinguish these instead of
// inspecting S3 error codes themselves.
var (
	// ErrNotFound means the key does not exist, or the credentials are not
	// allowed to know whether it exists.
	ErrNotFound = errors.New("object not found")
	// ErrAlreadyExists means a conditional create lost the race for a key.
	ErrAlreadyExists = errors.New("object already exists")
	// ErrAccessDenied means the backend refused the credentials. Writes
	// report it directly; reads deliberately do not, see mapError.
	ErrAccessDenied = errors.New("access denied by the storage backend")
	// ErrConditionalWriteUnsupported means the backend rejected
	// If-None-Match, which this tool relies on to never overwrite an upload.
	ErrConditionalWriteUnsupported = errors.New("backend does not support conditional object creation (If-None-Match: *)")
)

// Store is a handle on one bucket.
type Store struct {
	client *s3.Client
	bucket string
}

// New builds a Store from validated configuration.
//
// The client is constructed directly rather than through the shared AWS config
// loader, so ambient AWS_* environment variables and ~/.aws profiles can never
// silently redirect an upload to a different account.
func New(cfg config.Config) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.PathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// The SDK's own client is used rather than a bare http.Client,
		// because it refuses to follow any redirect other than 307/308 — a
		// 302 would otherwise turn a PutObject into a GET that "succeeds"
		// without storing anything.
		//
		// The bound is on time-to-first-byte rather than the whole request:
		// a whole-request deadline would cut the viewer off mid-stream while
		// it is legitimately sending a large object to a slow client.
		HTTPClient: awshttp.NewBuildableClient().WithTransportOptions(
			func(tr *http.Transport) { tr.ResponseHeaderTimeout = responseHeaderTimeout }),
		// Non-AWS backends commonly reject the flexible-checksum trailers the
		// SDK would otherwise add to every request. dkwws stores its own
		// SHA-256 in the link record instead.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// ObjectInfo describes a stored object without its body.
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// PutNew writes key only if it does not exist yet. Two uploads that pick the
// same key can therefore never overwrite each other: the loser gets
// ErrAlreadyExists and retries with a new identifier.
func (s *Store) PutNew(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(int64(len(body))),
		IfNoneMatch:   aws.String("*"),
	})
	if err != nil {
		return mapPutError(key, err)
	}
	return nil
}

// Get streams an object. The caller closes the returned reader.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, ObjectInfo{}, mapError(key, err)
	}
	return out.Body, ObjectInfo{
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

// Head reads an object's metadata.
func (s *Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return ObjectInfo{}, mapError(key, err)
	}
	return ObjectInfo{
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

// PutLink writes a new link record. It fails with ErrAlreadyExists if the
// token is somehow already taken.
func (s *Store) PutLink(ctx context.Context, token string, rec link.Record) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode link record: %w", err)
	}
	return s.PutNew(ctx, link.LinkKey(token), body, "application/json")
}

// maxRecordSize bounds how much of a link record is read back, so a wrong or
// hostile key cannot make the viewer buffer a large object.
const maxRecordSize = 64 << 10

// GetLink reads and validates a link record. A missing, unreadable or
// malformed record is reported as ErrNotFound so callers cannot use the
// difference to probe the bucket.
func (s *Store) GetLink(ctx context.Context, token string) (link.Record, error) {
	if !link.ValidToken(token) {
		return link.Record{}, ErrNotFound
	}
	body, _, err := s.Get(ctx, link.LinkKey(token))
	if err != nil {
		return link.Record{}, err
	}
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, maxRecordSize+1))
	if err != nil {
		return link.Record{}, fmt.Errorf("read link record: %w", err)
	}
	if len(data) > maxRecordSize {
		return link.Record{}, ErrNotFound
	}
	var rec link.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return link.Record{}, ErrNotFound
	}
	if err := rec.Validate(); err != nil {
		return link.Record{}, ErrNotFound
	}
	return rec, nil
}

// mapPutError translates write failures. A backend that does not implement
// If-None-Match answers NotImplemented rather than 412, and that has to
// surface as a configuration problem instead of a lost race.
//
// Unlike reads, a write reports a refused credential as exactly that: there is
// no bucket to probe on the way in, and "not found" would send an operator
// looking in the wrong place.
func mapPutError(key string, err error) error {
	switch statusOf(err) {
	case http.StatusPreconditionFailed, http.StatusConflict:
		return fmt.Errorf("%s: %w", key, ErrAlreadyExists)
	case http.StatusNotImplemented:
		return fmt.Errorf("%w: %v", ErrConditionalWriteUnsupported, err)
	}
	if apiCode(err) == "NotImplemented" {
		return fmt.Errorf("%w: %v", ErrConditionalWriteUnsupported, err)
	}
	// On a write there is no "the key might simply not exist" alternative,
	// so any refusal is a genuine permissions problem.
	if statusOf(err) == http.StatusForbidden || isDenialCode(apiCode(err)) {
		return fmt.Errorf("%s: %w: %v", key, ErrAccessDenied, err)
	}
	return fmt.Errorf("s3 request for %s failed: %w", key, err)
}

// mapError translates read failures. A refused credential is reported as
// ErrNotFound on purpose, so the viewer answers identically whether a key is
// missing or merely off-limits and cannot be used to probe the bucket. The
// original error stays in the chain so AccessDenied can still recognise it.
func mapError(key string, err error) error {
	switch statusOf(err) {
	case http.StatusNotFound, http.StatusForbidden:
		return fmt.Errorf("%s: %w", key, errors.Join(ErrNotFound, err))
	}
	switch apiCode(err) {
	case "NoSuchKey", "NotFound", "AccessDenied":
		return fmt.Errorf("%s: %w", key, errors.Join(ErrNotFound, err))
	}
	return fmt.Errorf("s3 request for %s failed: %w", key, err)
}

// CredentialsRejected reports whether the backend refused the credentials
// themselves. The viewer uses it to log a broken deployment loudly while still
// answering 404, which it could not do from ErrNotFound alone.
//
// A bare 403 or AccessDenied deliberately does not count. S3 answers GetObject
// for a missing key with 403 AccessDenied when the caller has no
// s3:ListBucket — which the documented viewer policy does not grant — so
// treating those as credential failures would raise an error for every unknown
// token and drown out the case this exists to surface.
func CredentialsRejected(err error) bool {
	switch apiCode(err) {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AuthorizationHeaderMalformed",
		"InvalidSecurity", "ExpiredToken", "TokenRefreshRequired":
		return true
	}
	return false
}

// isDenialCode covers the codes that mean "not allowed", including the
// ambiguous ones that only a write can interpret safely.
func isDenialCode(code string) bool {
	switch code {
	case "AccessDenied", "AllAccessDisabled", "InvalidAccessKeyId",
		"SignatureDoesNotMatch", "AuthorizationHeaderMalformed",
		"InvalidSecurity", "ExpiredToken", "TokenRefreshRequired":
		return true
	}
	return false
}

func statusOf(err error) int {
	var resp *smithyhttp.ResponseError
	if errors.As(err, &resp) && resp.Response != nil {
		return resp.HTTPStatusCode()
	}
	return 0
}

func apiCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}
