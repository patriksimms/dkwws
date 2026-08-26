// Package config loads S3 credentials and endpoint settings from the process
// environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment keys.
const (
	KeyEndpoint      = "DKWWS_S3_ENDPOINT"
	KeyRegion        = "DKWWS_S3_REGION"
	KeyBucket        = "DKWWS_S3_BUCKET"
	KeyAccessKeyID   = "DKWWS_S3_ACCESS_KEY_ID"
	KeySecretKey     = "DKWWS_S3_SECRET_ACCESS_KEY"
	KeyPathStyle     = "DKWWS_S3_PATH_STYLE"
	KeyPublicBaseURL = "DKWWS_PUBLIC_BASE_URL"
	KeyAllowInsecure = "DKWWS_S3_ALLOW_INSECURE"
)

// DefaultRegion is used when no region is configured. Most self-hosted
// S3-compatible backends ignore the region but SigV4 still has to sign one.
const DefaultRegion = "us-east-1"

// Config holds everything needed to talk to an S3-compatible backend.
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle selects path-style addressing (endpoint/bucket/key) over
	// virtual-hosted addressing. It defaults to true because self-hosted
	// backends rarely offer per-bucket subdomains.
	PathStyle bool
	// PublicBaseURL is where the bucket's public prefix is reachable. It is
	// optional: when empty it is derived from the endpoint and bucket. Set it
	// when the bucket is published on its own domain or behind a CDN.
	PublicBaseURL string
	// AllowInsecure permits a plain-http endpoint on a non-loopback host.
	AllowInsecure bool
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		Endpoint:        strings.TrimSpace(os.Getenv(KeyEndpoint)),
		Region:          strings.TrimSpace(os.Getenv(KeyRegion)),
		Bucket:          strings.TrimSpace(os.Getenv(KeyBucket)),
		AccessKeyID:     strings.TrimSpace(os.Getenv(KeyAccessKeyID)),
		SecretAccessKey: strings.TrimSpace(os.Getenv(KeySecretKey)),
		PathStyle:       true,
		PublicBaseURL:   strings.TrimSpace(os.Getenv(KeyPublicBaseURL)),
	}
	if cfg.Region == "" {
		cfg.Region = DefaultRegion
	}
	if value := strings.TrimSpace(os.Getenv(KeyPathStyle)); value != "" {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a boolean: %w", KeyPathStyle, err)
		}
		cfg.PathStyle = b
	}
	if value := strings.TrimSpace(os.Getenv(KeyAllowInsecure)); value != "" {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a boolean: %w", KeyAllowInsecure, err)
		}
		cfg.AllowInsecure = b
	}
	return cfg, nil
}
