package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patriksimms/dkwws/internal/config"
)

// isolate points the loader at a temporary configuration file and clears every
// DKWWS_* variable, so a developer's own settings cannot influence a test.
func isolate(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	t.Setenv(config.KeyConfigFile, path)
	for _, key := range []string{
		config.KeyEndpoint, config.KeyRegion, config.KeyBucket,
		config.KeyAccessKeyID, config.KeySecretKey, config.KeyPathStyle,
		config.KeyViewerBaseURL, config.KeyAllowInsecure,
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	return path
}

func writeConfig(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv(config.KeyEndpoint, "https://s3.example.com")
	t.Setenv(config.KeyBucket, "dkwws")
	t.Setenv(config.KeyAccessKeyID, "AKIA")
	t.Setenv(config.KeySecretKey, "secret")
	t.Setenv(config.KeyViewerBaseURL, "https://dkwws.example.com")

	cfg, _, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.ValidateForUpload(); err != nil {
		t.Fatalf("ValidateForUpload: %v", err)
	}
	if cfg.Region != config.DefaultRegion {
		t.Errorf("Region = %q, want the default %q", cfg.Region, config.DefaultRegion)
	}
	if !cfg.PathStyle {
		t.Error("PathStyle defaults to true for self-hosted backends")
	}
}

func TestLoadFromFile(t *testing.T) {
	path := isolate(t)
	writeConfig(t, path, `# credentials for the build box
export DKWWS_S3_ENDPOINT=https://s3.example.com
DKWWS_S3_BUCKET = "dkwws"
DKWWS_S3_ACCESS_KEY_ID='AKIA'
DKWWS_S3_SECRET_ACCESS_KEY=secret
DKWWS_S3_PATH_STYLE=false
DKWWS_VIEWER_BASE_URL=https://dkwws.example.com
`, 0o600)

	cfg, src, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src.FilePath != path {
		t.Errorf("FilePath = %q, want %q", src.FilePath, path)
	}
	if err := cfg.ValidateForUpload(); err != nil {
		t.Fatalf("ValidateForUpload: %v", err)
	}
	if cfg.Bucket != "dkwws" || cfg.AccessKeyID != "AKIA" || cfg.SecretAccessKey != "secret" {
		t.Errorf("parsed config = %+v", cfg)
	}
	if cfg.PathStyle {
		t.Error("PathStyle=false in the file was not honoured")
	}
}

// CI and direnv set variables in the environment; they must win over whatever
// happens to be on disk.
func TestEnvironmentOverridesFile(t *testing.T) {
	path := isolate(t)
	writeConfig(t, path, "DKWWS_S3_BUCKET=from-file\n", 0o600)
	t.Setenv(config.KeyBucket, "from-env")

	cfg, _, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Bucket != "from-env" {
		t.Errorf("Bucket = %q, want from-env", cfg.Bucket)
	}
}

func TestLoadRefusesWorldReadableConfigFile(t *testing.T) {
	path := isolate(t)
	writeConfig(t, path, "DKWWS_S3_BUCKET=dkwws\n", 0o644)

	_, _, err := config.Load()
	if err == nil {
		t.Fatal("Load accepted a config file readable by other users")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should say what mode is expected, got: %v", err)
	}
}

func TestLoadWithoutConfigFile(t *testing.T) {
	isolate(t)
	cfg, src, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src.FilePath != "" {
		t.Errorf("FilePath = %q, want empty when no file exists", src.FilePath)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an empty configuration")
	}
}

func TestValidateNamesEveryMissingKey(t *testing.T) {
	isolate(t)
	cfg, _, err := config.Load()
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

func TestValidateForUploadRequiresViewerBaseURL(t *testing.T) {
	cfg := config.Config{
		Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "dkwws",
		AccessKeyID: "AKIA", SecretAccessKey: "secret",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	err := cfg.ValidateForUpload()
	if err == nil || !strings.Contains(err.Error(), config.KeyViewerBaseURL) {
		t.Fatalf("ValidateForUpload error = %v, want it to name %s", err, config.KeyViewerBaseURL)
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

func TestFilePathHonoursXDG(t *testing.T) {
	t.Setenv(config.KeyConfigFile, "")
	os.Unsetenv(config.KeyConfigFile)
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	path, err := config.FilePath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg", "dkwws", "config"); path != want {
		t.Errorf("FilePath = %q, want %q", path, want)
	}
}
