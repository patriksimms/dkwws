// Package store wraps the one S3 operation this tool depends on: PutObject
// with conditional creation, signed with AWS Signature Version 4.
//
// Reading is not the tool's job. Uploaded files are served straight out of the
// bucket's public prefix, so nothing here ever fetches an object back.
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/patriksimms/dkwws/internal/config"
)

// responseHeaderTimeout bounds how long the backend may take to start
// answering. Without it a hung endpoint leaves the CLI blocked indefinitely.
const responseHeaderTimeout = 30 * time.Second

// Sentinel errors returned by the store.
var (
	// ErrAlreadyExists means a conditional create lost the race for a key.
	ErrAlreadyExists = errors.New("object already exists")
	// ErrAccessDenied means the backend refused the credentials.
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
		HTTPClient: awshttp.NewBuildableClient().WithTransportOptions(
			func(tr *http.Transport) { tr.ResponseHeaderTimeout = responseHeaderTimeout }),
		// Non-AWS backends commonly reject the flexible-checksum trailers the
		// SDK would otherwise add to every request.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// PutNew writes key only if it does not exist yet. Two uploads that somehow
// picked the same key can therefore never overwrite each other: the loser gets
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

// mapPutError translates write failures. A backend that does not implement
// If-None-Match answers NotImplemented rather than 412, and that has to
// surface as a configuration problem instead of a lost race.
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
	// A write has no "the key might simply not exist" alternative, so any
	// refusal is a genuine permissions problem and is reported as one.
	if statusOf(err) == http.StatusForbidden || isDenialCode(apiCode(err)) {
		return fmt.Errorf("%s: %w: %v", key, ErrAccessDenied, err)
	}
	return fmt.Errorf("s3 request for %s failed: %w", key, err)
}

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
