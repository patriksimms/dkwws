package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Validate checks the settings every component needs to reach the backend.
func (c Config) Validate() error {
	var missing []string
	for _, f := range []struct {
		key   string
		value string
	}{
		{KeyEndpoint, c.Endpoint},
		{KeyBucket, c.Bucket},
		{KeyAccessKeyID, c.AccessKeyID},
		{KeySecretKey, c.SecretAccessKey},
	} {
		if f.value == "" {
			missing = append(missing, f.key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return c.checkEndpoint()
}

// ValidateForUpload additionally requires the public viewer origin, which the
// CLI needs to turn a token into a shareable URL.
func (c Config) ValidateForUpload() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.ViewerBaseURL == "" {
		return fmt.Errorf("missing required configuration: %s", KeyViewerBaseURL)
	}
	u, err := url.Parse(c.ViewerBaseURL)
	if err != nil {
		return fmt.Errorf("%s: %w", KeyViewerBaseURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL such as https://dkwws.example.com, got %q", KeyViewerBaseURL, c.ViewerBaseURL)
	}
	return nil
}

// checkEndpoint refuses to send long-lived credentials over plain HTTP unless
// the endpoint is on this machine or the operator opted out explicitly.
func (c Config) checkEndpoint() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("%s: %w", KeyEndpoint, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL such as https://s3.example.com, got %q", KeyEndpoint, c.Endpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if c.AllowInsecure || isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New(KeyEndpoint + " uses http, which would send the secret access key in the clear; use https or set " + KeyAllowInsecure + "=true")
	default:
		return fmt.Errorf("%s must use http or https, got %q", KeyEndpoint, u.Scheme)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
