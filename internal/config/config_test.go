package config_test

import (
	"strings"
	"testing"

	"github.com/patriksimms/dkwws/internal/config"
)

// isolate clears every DKWWS_* variable so a developer's own settings cannot
// influence a test.
func isolate(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		config.KeyEndpoint, config.KeyRegion, config.KeyBucket,
		config.KeyAccessKeyID, config.KeySecretKey, config.KeyPathStyle,
		config.KeyPublicBaseURL, config.KeyAllowInsecure,
	} {
		t.Setenv(key, "")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv(config.KeyEndpoint, "https://s3.example.com")
	t.Setenv(config.KeyBucket, "dkwws")
	t.Setenv(config.KeyAccessKeyID, "AKIA")
	t.Setenv(config.KeySecretKey, "secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Region != config.DefaultRegion {
		t.Errorf("Region = %q, want the default %q", cfg.Region, config.DefaultRegion)
	}
	if !cfg.PathStyle {
		t.Error("PathStyle defaults to true for self-hosted backends")
	}
}

func TestLoadOptionalEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv(config.KeyRegion, "eu-central-1")
	t.Setenv(config.KeyPathStyle, "false")
	t.Setenv(config.KeyPublicBaseURL, "https://files.example.com")
	t.Setenv(config.KeyAllowInsecure, "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "eu-central-1" {
		t.Errorf("Region = %q, want eu-central-1", cfg.Region)
	}
	if cfg.PathStyle {
		t.Error("PathStyle = true, want false")
	}
	if cfg.PublicBaseURL != "https://files.example.com" {
		t.Errorf("PublicBaseURL = %q, want https://files.example.com", cfg.PublicBaseURL)
	}
	if !cfg.AllowInsecure {
		t.Error("AllowInsecure = false, want true")
	}
}

func TestValidateNamesEveryMissingKey(t *testing.T) {
	isolate(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty configuration")
	}
	for _, key := range []string{
		config.KeyEndpoint, config.KeyBucket, config.KeyAccessKeyID, config.KeySecretKey,
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

// The public address is derived from the endpoint and bucket, so a plain setup
// needs one fewer setting and cannot get the two out of step.
func TestPublicBaseIsDerivedFromTheEndpoint(t *testing.T) {
	base := config.Config{
		Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "dkwws",
		AccessKeyID: "AKIA", SecretAccessKey: "secret",
	}
	for _, tc := range []struct {
		name      string
		pathStyle bool
		override  string
		want      string
	}{
		{"path style", true, "", "https://s3.example.com/dkwws"},
		{"virtual host", false, "", "https://dkwws.s3.example.com"},
		{"explicit override", true, "https://files.example.com", "https://files.example.com"},
		{"override with trailing slash", true, "https://files.example.com/", "https://files.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.PathStyle = tc.pathStyle
			cfg.PublicBaseURL = tc.override
			got, err := cfg.PublicBase()
			if err != nil {
				t.Fatalf("PublicBase: %v", err)
			}
			if got != tc.want {
				t.Errorf("PublicBase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPublicBaseRejectsRelativeOverride(t *testing.T) {
	cfg := config.Config{
		Endpoint: "https://s3.example.com", Bucket: "dkwws",
		PublicBaseURL: "files.example.com",
	}
	if _, err := cfg.PublicBase(); err == nil || !strings.Contains(err.Error(), config.KeyPublicBaseURL) {
		t.Fatalf("PublicBase error = %v, want it to name %s", err, config.KeyPublicBaseURL)
	}
}

// Long-lived credentials must not go over plain HTTP to a remote host.
func TestValidateEndpointScheme(t *testing.T) {
	base := config.Config{
		Region: "us-east-1", Bucket: "dkwws",
		AccessKeyID: "AKIA", SecretAccessKey: "secret",
	}
	for _, tc := range []struct {
		endpoint      string
		allowInsecure bool
		wantErr       bool
	}{
		{"https://s3.example.com", false, false},
		{"http://s3.example.com", false, true},
		{"http://s3.example.com", true, false},
		{"http://localhost:9000", false, false},
		{"http://127.0.0.1:9000", false, false},
		{"ftp://s3.example.com", false, true},
		{"s3.example.com", false, true},
	} {
		cfg := base
		cfg.Endpoint = tc.endpoint
		cfg.AllowInsecure = tc.allowInsecure
		err := cfg.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("Validate(%q, insecure=%v) error = %v, wantErr %v",
				tc.endpoint, tc.allowInsecure, err, tc.wantErr)
		}
	}
}
