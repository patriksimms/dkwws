// Package config loads S3 credentials and endpoint settings from the process
// environment and an optional XDG configuration file.
//
// Both sources use the same DKWWS_* names, so a value can be moved between
// them without translation. The environment wins, which keeps CI and direnv
// setups working without touching the file on disk.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment and configuration-file keys.
const (
	KeyEndpoint      = "DKWWS_S3_ENDPOINT"
	KeyRegion        = "DKWWS_S3_REGION"
	KeyBucket        = "DKWWS_S3_BUCKET"
	KeyAccessKeyID   = "DKWWS_S3_ACCESS_KEY_ID"
	KeySecretKey     = "DKWWS_S3_SECRET_ACCESS_KEY"
	KeyPathStyle     = "DKWWS_S3_PATH_STYLE"
	KeyPublicBaseURL = "DKWWS_PUBLIC_BASE_URL"
	KeyAllowInsecure = "DKWWS_S3_ALLOW_INSECURE"

	// KeyConfigFile overrides the XDG configuration-file location.
	KeyConfigFile = "DKWWS_CONFIG_FILE"
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

// Source describes where a value came from, for diagnostics.
type Source struct {
	// FilePath is the configuration file that was read, empty if none was.
	FilePath string
}

// Load reads the configuration file (when present) and overlays the
// environment on top of it.
func Load() (Config, Source, error) {
	var src Source

	path := FilePath()
	values, err := readFile(path)
	if err != nil {
		return Config{}, src, err
	}
	if values != nil {
		src.FilePath = path
	}
	if values == nil {
		values = map[string]string{}
	}
	for _, key := range []string{
		KeyEndpoint, KeyRegion, KeyBucket, KeyAccessKeyID, KeySecretKey,
		KeyPathStyle, KeyPublicBaseURL, KeyAllowInsecure,
	} {
		if v, ok := os.LookupEnv(key); ok {
			values[key] = v
		}
	}

	cfg := Config{
		Endpoint:        strings.TrimSpace(values[KeyEndpoint]),
		Region:          strings.TrimSpace(values[KeyRegion]),
		Bucket:          strings.TrimSpace(values[KeyBucket]),
		AccessKeyID:     strings.TrimSpace(values[KeyAccessKeyID]),
		SecretAccessKey: strings.TrimSpace(values[KeySecretKey]),
		PathStyle:       true,
		PublicBaseURL:   strings.TrimSpace(values[KeyPublicBaseURL]),
	}
	if cfg.Region == "" {
		cfg.Region = DefaultRegion
	}
	if v, ok := values[KeyPathStyle]; ok && strings.TrimSpace(v) != "" {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return Config{}, src, fmt.Errorf("%s must be a boolean: %w", KeyPathStyle, err)
		}
		cfg.PathStyle = b
	}
	if v, ok := values[KeyAllowInsecure]; ok && strings.TrimSpace(v) != "" {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return Config{}, src, fmt.Errorf("%s must be a boolean: %w", KeyAllowInsecure, err)
		}
		cfg.AllowInsecure = b
	}
	return cfg, src, nil
}

// FilePath returns the configuration-file location, honouring
// DKWWS_CONFIG_FILE and then XDG_CONFIG_HOME.
//
// It returns an empty path when there is no home directory to look in. The
// file is optional, and the viewer's container image sets no HOME, so a
// missing home must not stop a fully environment-configured process.
func FilePath() string {
	if v := strings.TrimSpace(os.Getenv(KeyConfigFile)); v != "" {
		return v
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "dkwws", "config")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "dkwws", "config")
}

// readFile parses a KEY=value file. It returns a nil map when the file does
// not exist, and refuses to read a file that is readable by anyone but its
// owner, because it holds long-lived credentials.
func readFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat config file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("config file %s holds credentials and must not be group- or world-accessible: mode is %04o, want 0600", path, perm)
	}

	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=value", path, lineNo)
		}
		values[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return values, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
